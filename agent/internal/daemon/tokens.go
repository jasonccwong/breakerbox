package daemon

import (
	"path/filepath"
	"strings"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// resolveAppByCwd maps a transcript working directory to a registered app:
// longest matching app-cwd prefix wins, symlinks resolved on both sides.
// "" means unmatched — the hub attributes those rows to the system so no
// spend is silently dropped.
func (d *Daemon) resolveAppByCwd(cwd string) string {
	if cwd == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = r
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	best, bestLen := "", 0
	for id, app := range d.state.Apps {
		dir := app.Definition.Cwd
		if dir == "" {
			continue
		}
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			dir = r
		}
		if (cwd == dir || strings.HasPrefix(cwd, dir+string(filepath.Separator))) && len(dir) > bestLen {
			best, bestLen = id, len(dir)
		}
	}
	return best
}

// emitTokenRows ships a batch to the hub; false = offline, watcher retries.
func (d *Daemon) emitTokenRows(rows []protocol.TokenUsageRow) bool {
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	if conn == nil {
		return false
	}
	return conn.Send(protocol.TypeTokenUsageBatch, protocol.TokenUsageBatch{Rows: rows}) == nil
}

// maxProxyBuffer bounds offline-buffered runtime-proxy rows.
const maxProxyBuffer = 5000

// queueProxyRows is the tokenproxy emit sink: try immediate delivery, buffer
// while offline (flushed by flushProxyRows on the metrics cadence).
func (d *Daemon) queueProxyRows(rows []protocol.TokenUsageRow) {
	if d.emitTokenRows(rows) {
		return
	}
	d.mu.Lock()
	d.proxyRowsBuf = append(d.proxyRowsBuf, rows...)
	if over := len(d.proxyRowsBuf) - maxProxyBuffer; over > 0 {
		d.proxyRowsBuf = d.proxyRowsBuf[over:]
	}
	d.mu.Unlock()
}

// flushProxyRows retries buffered proxy rows once a connection exists.
func (d *Daemon) flushProxyRows() {
	d.mu.Lock()
	buf := d.proxyRowsBuf
	d.proxyRowsBuf = nil
	d.mu.Unlock()
	if len(buf) > 0 && !d.emitTokenRows(buf) {
		d.mu.Lock()
		d.proxyRowsBuf = append(buf, d.proxyRowsBuf...)
		d.mu.Unlock()
	}
}
