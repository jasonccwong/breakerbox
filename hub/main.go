// breakerbox-hub is the central BreakerBox server: PocketBase (embedded
// SQLite, auth, REST, realtime) plus the agent WebSocket plane and the
// embedded web UI.
package main

import (
	"log"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/migratecmd"

	"github.com/breakerbox/breakerbox/hub/internal/agenthub"
	"github.com/breakerbox/breakerbox/hub/internal/webassets"
	_ "github.com/breakerbox/breakerbox/hub/migrations"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	app := pocketbase.New()

	migratecmd.MustRegister(app, app.RootCmd, migratecmd.Config{
		// Registered Go migrations auto-apply on serve; no dev automigrate
		// since the schema is maintained as code in hub/migrations.
		Automigrate: false,
	})

	agenthub.Register(app)

	// Serve the embedded web SPA at the root (when a build is embedded).
	if spa, ok := webassets.Handler(); ok {
		app.OnServe().BindFunc(func(se *core.ServeEvent) error {
			se.Router.GET("/{path...}", func(e *core.RequestEvent) error {
				spa.ServeHTTP(e.Response, e.Request)
				return nil
			})
			return se.Next()
		})
	}

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
