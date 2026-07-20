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
