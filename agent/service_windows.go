//go:build windows

package main

// Windows Service integration: when launched by the Service Control Manager,
// `breakerbox-agent run` must speak the SCM protocol instead of blocking on
// posix signals. Detection is automatic — the same binary works from a
// console and as a service.
//
// Known platform scope: services run in session 0, so supervised GUI apps
// won't show a window. BreakerBox targets server-style workloads; see
// docs/windows.md.

import (
	"context"
	"log/slog"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"github.com/breakerbox/breakerbox/agent/internal/appconfig"
	"github.com/breakerbox/breakerbox/agent/internal/daemon"
	"github.com/breakerbox/breakerbox/agent/internal/identity"
)

const serviceName = "breakerbox-agent"

// maybeRunAsService returns (true, err) when the process is under the SCM and
// the service lifecycle ran to completion; (false, nil) for console runs.
func maybeRunAsService(store *appconfig.Store) (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false, nil
	}
	// Route logs to the Windows event log; stdout goes nowhere in session 0.
	if elog, err := eventlog.Open(serviceName); err == nil {
		defer elog.Close()
		slog.SetDefault(slog.New(newEventLogHandler(elog)))
	}
	return true, svc.Run(serviceName, &agentService{store: store})
}

type agentService struct {
	store *appconfig.Store
}

func (s *agentService) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	priv, err := identity.LoadOrCreate(s.store.Dir)
	if err != nil {
		slog.Error("service start: identity", "err", err)
		return false, 1
	}
	d, err := daemon.New(s.store, priv, version)
	if err != nil {
		slog.Error("service start: daemon", "err", err)
		return false, 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		case err := <-done:
			// Daemon exited on its own — treat as failure so SCM restarts us.
			cancel()
			if err != nil {
				slog.Error("daemon exited", "err", err)
				return false, 1
			}
			return false, 0
		}
	}
}

// eventLogHandler bridges slog to the Windows event log.
type eventLogHandler struct {
	elog *eventlog.Log
	attr string
}

func newEventLogHandler(elog *eventlog.Log) *eventLogHandler { return &eventLogHandler{elog: elog} }

func (h *eventLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h *eventLogHandler) Handle(_ context.Context, r slog.Record) error {
	msg := r.Message
	r.Attrs(func(a slog.Attr) bool {
		msg += " " + a.Key + "=" + a.Value.String()
		return true
	})
	if h.attr != "" {
		msg = h.attr + " " + msg
	}
	switch {
	case r.Level >= slog.LevelError:
		return h.elog.Error(1, msg)
	case r.Level >= slog.LevelWarn:
		return h.elog.Warning(1, msg)
	default:
		return h.elog.Info(1, msg)
	}
}

func (h *eventLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	for _, a := range attrs {
		nh.attr += a.Key + "=" + a.Value.String() + " "
	}
	return &nh
}

func (h *eventLogHandler) WithGroup(string) slog.Handler { return h }
