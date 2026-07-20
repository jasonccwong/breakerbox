package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/breakerbox/breakerbox/agent/internal/appconfig"
	"github.com/breakerbox/breakerbox/pkg/protocol"
)

// metricsLoop samples and ships metrics every metricsInterval.
func (d *Daemon) metricsLoop(ctx context.Context) {
	tick := time.NewTicker(metricsInterval)
	defer tick.Stop()
	// Prime CPU counters so the first real sample has a delta.
	d.col.HostSample()
	for {
		select {
		case <-tick.C:
			batch := protocol.MetricsBatch{Host: []protocol.HostSample{d.col.HostSample()}}
			d.mu.Lock()
			running := map[string]int{}
			for id, p := range d.procs {
				if p.Alive() {
					running[id] = p.PID()
				}
			}
			d.mu.Unlock()
			for id, pid := range running {
				batch.Apps = append(batch.Apps, d.col.AppSample(id, pid))
			}
			if len(batch.Host) > 0 {
				batch.Apps = append(batch.Apps, d.dockerAppSamples(batch.Host[0].TS)...)
			}
			d.sendMetrics(batch)
			d.flushProxyRows()
		case <-ctx.Done():
			return
		}
	}
}

// maxBufferedBatches bounds the offline metrics buffer: at one batch per 30s
// this holds roughly two hours of history across a hub outage.
const maxBufferedBatches = 240

// sendMetrics ships a batch, buffering while offline and flushing the backlog
// (coalesced into one frame) once a connection exists again.
func (d *Daemon) sendMetrics(batch protocol.MetricsBatch) {
	d.mu.Lock()
	conn := d.conn
	if conn == nil {
		d.metricsBuf = append(d.metricsBuf, batch)
		if over := len(d.metricsBuf) - maxBufferedBatches; over > 0 {
			d.metricsBuf = d.metricsBuf[over:]
		}
		d.mu.Unlock()
		return
	}
	buffered := d.metricsBuf
	d.metricsBuf = nil
	d.mu.Unlock()

	if len(buffered) > 0 {
		merged := protocol.MetricsBatch{}
		for _, b := range buffered {
			merged.Host = append(merged.Host, b.Host...)
			merged.Apps = append(merged.Apps, b.Apps...)
		}
		slog.Info("flushing buffered offline metrics", "batches", len(buffered))
		if err := conn.Send(protocol.TypeMetrics, merged); err != nil {
			slog.Debug("flush buffered metrics", "err", err)
		}
	}
	if err := conn.Send(protocol.TypeMetrics, batch); err != nil {
		slog.Debug("send metrics", "err", err)
	}
}

// spoolLoop consumes CLI operations (import/approve/reject) from the spool.
func (d *Daemon) spoolLoop(ctx context.Context) {
	tick := time.NewTicker(spoolInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			ops, err := d.store.Drain()
			if err != nil {
				slog.Error("drain spool", "err", err)
				continue
			}
			for _, op := range ops {
				d.applySpoolOp(op)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (d *Daemon) applySpoolOp(op appconfig.SpoolOp) {
	switch op.Op {
	case "import":
		if op.Definition == nil {
			return
		}
		slog.Info("importing app definition from host", "name", op.Definition.Name)
		// The hub assigns the ID and echoes it back via app_sync; because
		// the import happened on this machine, we pre-approve its hash so
		// the app_sync reconciliation can keep it approved.
		d.mu.Lock()
		d.preApproved = append(d.preApproved, op.Definition.Hash())
		d.mu.Unlock()
		d.send(protocol.TypeAppRegister, protocol.AppRegister{Definition: *op.Definition})
	case "approve", "reject":
		d.mu.Lock()
		app, ok := d.state.Apps[op.AppID]
		if !ok {
			d.mu.Unlock()
			slog.Warn("spool op for unknown app", "op", op.Op, "app", op.AppID)
			return
		}
		if op.Op == "approve" {
			app.Approval = protocol.ApprovalApproved
		} else {
			app.Approval = protocol.ApprovalRejected
		}
		d.state.Apps[op.AppID] = app
		_ = d.store.Save(d.state)
		d.mu.Unlock()
		slog.Info("approval updated on host", "app", op.AppID, "approval", app.Approval)
		d.send(protocol.TypeApprovalEvent, protocol.ApprovalEvent{
			AppID: op.AppID, Approval: app.Approval, DefinitionHash: app.Hash,
		})
	default:
		slog.Warn("unknown spool op", "op", op.Op)
	}
}
