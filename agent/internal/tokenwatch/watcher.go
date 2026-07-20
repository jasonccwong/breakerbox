package tokenwatch

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

const (
	scanInterval = 15 * time.Second
	// backfillWindow bounds the first-run history import.
	backfillWindow = 30 * 24 * time.Hour
	// maxBackfillSize skips full parse of pathological files (Codex rollouts
	// can reach GBs); tailing starts at the end instead.
	maxBackfillSize = 50 << 20
	flushEvery      = 5 * time.Second
	maxLineBytes    = 4 << 20 // Claude assistant lines can be large
)

// Resolver maps a transcript working directory to an app ID ("" = unmatched).
type Resolver func(cwd string) string

// Emit receives batched usage rows. Returning false means "not delivered,
// retry later" — the watcher then re-emits the same rows next flush and holds
// file offsets back so nothing is lost across restarts either.
type Emit func(rows []protocol.TokenUsageRow) bool

// Watcher tails coding-agent transcript files and emits usage rows.
type Watcher struct {
	stateDir string
	resolve  Resolver
	emit     Emit

	checkpoints map[string]*checkpoint // file path -> progress
	pending     []protocol.TokenUsageRow
	// lastOutput tracks the max output_tokens emitted per dedup key so
	// streaming rewrites of the same message only re-emit on growth.
	lastOutput map[string]int64
	unparsed   int
}

type checkpoint struct {
	Offset int64       `json:"offset"`
	Codex  *codexState `json:"codex,omitempty"`
}

// New creates a watcher persisting checkpoints under stateDir.
func New(stateDir string, resolve Resolver, emit Emit) *Watcher {
	w := &Watcher{
		stateDir:    stateDir,
		resolve:     resolve,
		emit:        emit,
		checkpoints: map[string]*checkpoint{},
		lastOutput:  map[string]int64{},
	}
	w.loadCheckpoints()
	return w
}

// Run blocks, scanning on an interval until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	// First pass immediately so a fresh install shows history fast.
	w.scan()
	w.flush()
	scanTick := time.NewTicker(scanInterval)
	flushTick := time.NewTicker(flushEvery)
	defer scanTick.Stop()
	defer flushTick.Stop()
	for {
		select {
		case <-ctx.Done():
			w.saveCheckpoints()
			return
		case <-scanTick.C:
			w.scan()
		case <-flushTick.C:
			w.flush()
		}
	}
}

// UnparsedCount reports lines that failed to parse (surfaced, never fatal).
func (w *Watcher) UnparsedCount() int { return w.unparsed }

// roots returns the transcript directories to watch.
func roots() (claude, codex string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".codex", "sessions")
}

func (w *Watcher) scan() {
	claudeRoot, codexRoot := roots()
	for _, f := range findJSONL(claudeRoot, 2) {
		w.tailFile(f, "claude")
	}
	for _, f := range findJSONL(codexRoot, 4) {
		w.tailFile(f, "codex")
	}
}

// findJSONL lists .jsonl files up to depth levels below root, modified within
// the backfill window (older files can't gain new lines and were either
// already imported or predate the install).
func findJSONL(root string, depth int) []string {
	if root == "" {
		return nil
	}
	cutoff := time.Now().Add(-backfillWindow)
	var out []string
	walk(root, depth, func(path string, info os.FileInfo) {
		if strings.HasSuffix(path, ".jsonl") && info.ModTime().After(cutoff) {
			out = append(out, path)
		}
	})
	return out
}

func walk(dir string, depth int, fn func(string, os.FileInfo)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if depth > 0 {
				walk(path, depth-1, fn)
			}
			continue
		}
		if info, err := e.Info(); err == nil {
			fn(path, info)
		}
	}
}

// tailFile reads new lines of one transcript since its checkpoint.
func (w *Watcher) tailFile(path, kind string) {
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	cp := w.checkpoints[path]
	if cp == nil {
		cp = &checkpoint{}
		if st.Size() > maxBackfillSize {
			cp.Offset = st.Size() // too big to backfill; watch new lines only
			slog.Warn("transcript too large for backfill; tailing from end", "path", path, "size", st.Size())
		}
		if kind == "codex" {
			cp.Codex = &codexState{}
		}
		w.checkpoints[path] = cp
	}
	if st.Size() < cp.Offset {
		// Truncated/rewritten: start over.
		cp.Offset = 0
		if cp.Codex != nil {
			cp.Codex = &codexState{}
		}
	}
	if st.Size() == cp.Offset {
		return
	}

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(cp.Offset, 0); err != nil {
		return
	}
	r := bufio.NewReaderSize(f, 256*1024)
	offset := cp.Offset
	for {
		line, err := readLine(r)
		if err != nil {
			break // includes clean EOF and a partial final line (no \n yet)
		}
		offset += int64(len(line))
		w.consumeLine(path, kind, cp, trimNL(line))
	}
	cp.Offset = offset
	// Checkpoints persist in flush(), only once parsed rows were delivered:
	// a crash here re-reads from the old offset and the hub's dedup absorbs
	// the duplicates. Losing unemitted rows would be the worse trade.
}

// readLine returns one complete line including its trailing \n, or an error
// when no complete line is available (partial writes stay for the next pass).
func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > maxLineBytes {
		return line[:0], nil // absurd line: count as consumed, parse nothing
	}
	return line, nil
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (w *Watcher) consumeLine(path, kind string, cp *checkpoint, line []byte) {
	if len(line) == 0 {
		return
	}
	switch kind {
	case "claude":
		row, ok := parseClaudeLine(line)
		if !ok {
			if json.Valid(line) {
				return // valid line of a type we don't consume
			}
			w.unparsed++
			return
		}
		// Streaming rewrites: only emit when output grew.
		if prev, seen := w.lastOutput[row.DedupKey]; seen && row.OutputTokens <= prev {
			return
		}
		w.lastOutput[row.DedupKey] = row.OutputTokens
		row.AppID = w.resolve(claudeCwd(line))
		w.pending = append(w.pending, row)
	case "codex":
		if cp.Codex == nil {
			cp.Codex = &codexState{}
		}
		row, ok := parseCodexLine(line, cp.Codex)
		if !ok {
			if !json.Valid(line) {
				w.unparsed++
			}
			return
		}
		row.AppID = w.resolve(cp.Codex.Cwd)
		w.pending = append(w.pending, row)
	}
	if len(w.pending) >= 500 {
		w.flush()
	}
}

func (w *Watcher) flush() {
	if len(w.pending) == 0 {
		w.saveCheckpoints() // offsets may have advanced past non-usage lines
		return
	}
	if w.emit(w.pending) {
		w.pending = nil
		w.saveCheckpoints()
		// Bound the streaming-dedup map; keys older than the current session
		// mix won't recur once files go quiet.
		if len(w.lastOutput) > 50_000 {
			w.lastOutput = map[string]int64{}
		}
	} else if len(w.pending) > 10_000 {
		slog.Warn("token usage backlog capped while hub offline", "dropped", len(w.pending)-10_000)
		w.pending = w.pending[len(w.pending)-10_000:]
	}
}

func (w *Watcher) checkpointPath() string {
	return filepath.Join(w.stateDir, "tokenwatch.json")
}

func (w *Watcher) loadCheckpoints() {
	b, err := os.ReadFile(w.checkpointPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &w.checkpoints)
}

func (w *Watcher) saveCheckpoints() {
	b, err := json.Marshal(w.checkpoints)
	if err != nil {
		return
	}
	tmp := w.checkpointPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, w.checkpointPath())
	}
}
