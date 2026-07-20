// Package setup implements first-run bootstrap: creating the first user
// account without visiting the PocketBase admin UI. The endpoint only works
// while the users collection is empty.
package setup

import (
	"net/http"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// Register attaches the first-run routes.
func Register(app core.App) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Report whether first-run setup is needed.
		se.Router.GET("/api/bb/setup", func(e *core.RequestEvent) error {
			n, err := e.App.CountRecords("users")
			if err != nil {
				return apis.NewInternalServerError("", err)
			}
			return e.JSON(http.StatusOK, map[string]any{"needs_setup": n == 0})
		})

		// Create the first user (only while no users exist).
		se.Router.POST("/api/bb/setup", func(e *core.RequestEvent) error {
			n, err := e.App.CountRecords("users")
			if err != nil {
				return apis.NewInternalServerError("", err)
			}
			if n > 0 {
				return apis.NewForbiddenError("setup already completed", nil)
			}
			var body struct {
				Email    string `json:"email"`
				Password string `json:"password"`
			}
			if err := e.BindBody(&body); err != nil {
				return apis.NewBadRequestError("invalid body", err)
			}
			col, err := e.App.FindCollectionByNameOrId("users")
			if err != nil {
				return apis.NewInternalServerError("", err)
			}
			rec := core.NewRecord(col)
			rec.SetEmail(body.Email)
			rec.SetPassword(body.Password)
			rec.SetVerified(true)
			if err := e.App.Save(rec); err != nil {
				return apis.NewBadRequestError("could not create user", err)
			}
			return e.JSON(http.StatusOK, map[string]any{"created": true})
		})
		return se.Next()
	})
}
