// Package notify pushes alert events to the user's ntfy endpoint. ntfy was
// chosen as the v1 push channel because it needs zero project-run
// infrastructure: the user self-hosts ntfy or uses ntfy.sh, and the mobile
// ntfy app delivers the push. It watches PB record transitions rather than
// hooking the agent plane directly, so alerting stays decoupled from
// transport details.
package notify

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"

	"github.com/breakerbox/breakerbox/pkg/protocol"
)

const (
	// appAlertDebounce suppresses repeat alerts for the same app: a crash
	// loop transitions into backoff on every attempt.
	appAlertDebounce = 5 * time.Minute
	// offlineGrace delays "system offline" alerts so agent restarts and
	// blips (reconnect takes seconds) do not page anyone.
	offlineGrace = 2 * time.Minute
)

// Notifier sends ntfy messages per the stored settings.
type Notifier struct {
	app core.App

	mu          sync.Mutex
	lastAppSent map[string]time.Time  // app ID -> last alert
	offlineAt   map[string]*time.Timer // system ID -> pending grace timer
	notified    map[string]bool        // system ID -> offline alert was sent
}

// Register wires record-transition hooks and the test route.
func Register(app core.App) *Notifier {
	n := &Notifier{
		app:         app,
		lastAppSent: map[string]time.Time{},
		offlineAt:   map[string]*time.Timer{},
		notified:    map[string]bool{},
	}

	app.OnRecordAfterUpdateSuccess("apps").BindFunc(func(e *core.RecordEvent) error {
		n.onAppUpdate(e.Record)
		return e.Next()
	})
	app.OnRecordAfterUpdateSuccess("systems").BindFunc(func(e *core.RecordEvent) error {
		n.onSystemUpdate(e.Record)
		return e.Next()
	})

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		se.Router.POST("/api/bb/notify/test", func(e *core.RequestEvent) error {
			if e.Auth == nil {
				return apis.NewUnauthorizedError("authentication required", nil)
			}
			if err := n.send("BreakerBox test", "Notifications are working 🎉", "default", "white_check_mark"); err != nil {
				return apis.NewBadRequestError(err.Error(), nil)
			}
			return e.JSON(http.StatusOK, map[string]string{"status": "sent"})
		}).Bind(apis.RequireAuth())
		return se.Next()
	})

	return n
}

// onAppUpdate alerts when an app lands in errored or backoff.
func (n *Notifier) onAppUpdate(rec *core.Record) {
	status := protocol.AppStatus(rec.GetString("status"))
	if status != protocol.StatusErrored && status != protocol.StatusBackoff {
		// Healthy again: clear the debounce so the next incident alerts
		// immediately.
		if status == protocol.StatusRunning {
			n.mu.Lock()
			delete(n.lastAppSent, rec.Id)
			n.mu.Unlock()
		}
		return
	}
	if !n.enabled("notify_app_errors") {
		return
	}

	n.mu.Lock()
	last, seen := n.lastAppSent[rec.Id]
	if seen && time.Since(last) < appAlertDebounce {
		n.mu.Unlock()
		return
	}
	n.lastAppSent[rec.Id] = time.Now()
	n.mu.Unlock()

	name := rec.GetString("name")
	var title, tags string
	if status == protocol.StatusBackoff {
		title = fmt.Sprintf("%s is crash-looping", name)
		tags = "warning"
	} else {
		title = fmt.Sprintf("%s failed", name)
		tags = "rotating_light"
	}
	body := fmt.Sprintf("App %q on %s entered status %s.", name, n.systemName(rec.GetString("system")), status)
	if err := n.send(title, body, "high", tags); err != nil {
		slog.Warn("ntfy send failed", "err", err)
	}
}

// onSystemUpdate arms/disarms the offline grace timer.
func (n *Notifier) onSystemUpdate(rec *core.Record) {
	id := rec.Id
	status := rec.GetString("status")

	n.mu.Lock()
	defer n.mu.Unlock()
	switch status {
	case "online":
		if t, ok := n.offlineAt[id]; ok {
			t.Stop()
			delete(n.offlineAt, id)
		}
		if n.notified[id] {
			delete(n.notified, id)
			name := rec.GetString("name")
			go func() {
				if err := n.send(fmt.Sprintf("%s is back online", name),
					fmt.Sprintf("System %q reconnected to the hub.", name), "default", "green_circle"); err != nil {
					slog.Warn("ntfy send failed", "err", err)
				}
			}()
		}
	case "offline":
		if _, pending := n.offlineAt[id]; pending || !n.enabled("notify_system_offline") {
			return
		}
		name := rec.GetString("name")
		n.offlineAt[id] = time.AfterFunc(offlineGrace, func() {
			n.mu.Lock()
			delete(n.offlineAt, id)
			n.mu.Unlock()
			// Re-check: it may have reconnected without the timer firing
			// (e.g. hook missed) — trust the record, not the timer.
			cur, err := n.app.FindRecordById("systems", id)
			if err != nil || cur.GetString("status") != "offline" {
				return
			}
			n.mu.Lock()
			n.notified[id] = true
			n.mu.Unlock()
			if err := n.send(fmt.Sprintf("%s went offline", name),
				fmt.Sprintf("System %q has been unreachable for %s.", name, offlineGrace), "high", "red_circle"); err != nil {
				slog.Warn("ntfy send failed", "err", err)
			}
		})
	}
}

// enabled reads one settings toggle (and requires a configured endpoint).
func (n *Notifier) enabled(field string) bool {
	s := n.settings()
	return s != nil && s.GetString("ntfy_endpoint") != "" && s.GetBool(field)
}

func (n *Notifier) settings() *core.Record {
	recs, err := n.app.FindRecordsByFilter("settings", "", "", 1, 0, nil)
	if err != nil || len(recs) == 0 {
		return nil
	}
	return recs[0]
}

func (n *Notifier) systemName(id string) string {
	if rec, err := n.app.FindRecordById("systems", id); err == nil {
		return rec.GetString("name")
	}
	return "unknown system"
}

// send posts one message to the configured ntfy endpoint.
func (n *Notifier) send(title, body, priority, tags string) error {
	s := n.settings()
	if s == nil {
		return fmt.Errorf("settings record missing")
	}
	endpoint := strings.TrimSpace(s.GetString("ntfy_endpoint"))
	if endpoint == "" {
		return fmt.Errorf("no ntfy endpoint configured")
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %s", resp.Status)
	}
	return nil
}
