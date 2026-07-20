package daemon

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/breakerbox/breakerbox/agent/internal/dockerapp"
	"github.com/breakerbox/breakerbox/pkg/protocol"
)

const (
	logPollInterval = 500 * time.Millisecond
	maxChunkLines   = 200
	// maxLogFileSize triggers rotation (one .1 generation) on app start.
	maxLogFileSize = 5 << 20
)

// startLogStream begins serving one log_follow request. Streams live until
// log_cancel, connection loss, or app log EOF.
func (d *Daemon) startLogStream(req protocol.LogFollow) {
	d.mu.Lock()
	app, ok := d.state.Apps[req.AppID]
	if d.logStreams == nil {
		d.logStreams = map[string]context.CancelFunc{}
	}
	if cancel, dup := d.logStreams[req.StreamID]; dup {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.logStreams[req.StreamID] = cancel
	d.mu.Unlock()

	if !ok {
		d.sendLogChunk(req.StreamID, []string{"[breakerbox] unknown app"}, true)
		cancel()
		return
	}

	emit := func(lines []string) { d.sendLogChunk(req.StreamID, lines, false) }
	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.logStreams, req.StreamID)
			d.mu.Unlock()
			d.sendLogChunk(req.StreamID, nil, true)
		}()
		switch app.Definition.Kind {
		case protocol.KindDocker:
			if d.docker == nil {
				emit([]string{"[breakerbox] docker unavailable on this host"})
				return
			}
			_ = d.docker.Follow(ctx, app.Definition.ContainerID, req.TailN, emit)
		case protocol.KindCompose:
			_ = dockerapp.ComposeFollow(ctx, app.Definition.ComposeProject, req.TailN, emit)
		default:
			d.followFile(ctx, req.AppID, req.TailN, emit)
		}
	}()
}

// cancelLogStream stops one stream (log_cancel from hub).
func (d *Daemon) cancelLogStream(streamID string) {
	d.mu.Lock()
	cancel, ok := d.logStreams[streamID]
	delete(d.logStreams, streamID)
	d.mu.Unlock()
	if ok {
		cancel()
	}
}

// cancelAllLogStreams runs on connection loss: the hub-side consumers are gone.
func (d *Daemon) cancelAllLogStreams() {
	d.mu.Lock()
	streams := d.logStreams
	d.logStreams = map[string]context.CancelFunc{}
	d.mu.Unlock()
	for _, cancel := range streams {
		cancel()
	}
}

func (d *Daemon) sendLogChunk(streamID string, lines []string, eof bool) {
	d.send(protocol.TypeLogChunk, protocol.LogChunk{StreamID: streamID, Lines: lines, EOF: eof})
}

// followFile tails a process app's log file: last tailN lines first, then new
// content by offset polling (we own the writer, so polling is reliable and
// avoids a platform fsnotify dependency for this one file).
func (d *Daemon) followFile(ctx context.Context, appID string, tailN int, emit func([]string)) {
	path := d.logPath(appID)
	if tailN <= 0 {
		tailN = 200
	}

	offset := int64(0)
	if b, err := os.ReadFile(path); err == nil {
		offset = int64(len(b))
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(lines) > tailN {
			lines = lines[len(lines)-tailN:]
		}
		if len(lines) > 0 && lines[0] != "" || len(lines) > 1 {
			emit(lines)
		}
	}

	tick := time.NewTicker(logPollInterval)
	defer tick.Stop()
	var partial string
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			st, err := f.Stat()
			if err != nil {
				f.Close()
				continue
			}
			if st.Size() < offset {
				offset = 0 // rotated/truncated: restart from the top
				partial = ""
			}
			if st.Size() == offset {
				f.Close()
				continue
			}
			if _, err := f.Seek(offset, 0); err != nil {
				f.Close()
				continue
			}
			buf := make([]byte, st.Size()-offset)
			n, _ := f.Read(buf)
			f.Close()
			offset += int64(n)

			text := partial + string(buf[:n])
			parts := strings.Split(text, "\n")
			partial = parts[len(parts)-1] // last element is incomplete (or "")
			lines := parts[:len(parts)-1]
			for len(lines) > 0 {
				chunk := lines
				if len(chunk) > maxChunkLines {
					chunk = lines[:maxChunkLines]
				}
				emit(chunk)
				lines = lines[len(chunk):]
			}
		}
	}
}

// rotateLogIfNeeded caps a process app's log file, keeping one generation.
func rotateLogIfNeeded(path string) {
	st, err := os.Stat(path)
	if err != nil || st.Size() < maxLogFileSize {
		return
	}
	if err := os.Rename(path, path+".1"); err != nil {
		slog.Warn("log rotation failed", "path", path, "err", err)
	}
}
