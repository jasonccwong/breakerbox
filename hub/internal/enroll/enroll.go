// Package enroll implements agent enrollment: authenticated users mint
// one-time tokens; agents redeem a token together with their Ed25519 public
// key to create their systems record. The raw token is returned exactly once
// and only its SHA-256 is stored.
package enroll

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// tokenTTL is how long a minted enrollment token stays valid.
const tokenTTL = 30 * time.Minute

// Register attaches the enrollment routes.
func Register(app core.App) {
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		// Mint a one-time enrollment token (authenticated users only).
		se.Router.POST("/api/bb/enroll-tokens", func(e *core.RequestEvent) error {
			if e.Auth == nil {
				return apis.NewUnauthorizedError("authentication required", nil)
			}
			raw := make([]byte, 24)
			if _, err := rand.Read(raw); err != nil {
				return apis.NewInternalServerError("token generation failed", err)
			}
			token := "bbe_" + base64.RawURLEncoding.EncodeToString(raw)

			col, err := e.App.FindCollectionByNameOrId("enroll_tokens")
			if err != nil {
				return apis.NewInternalServerError("", err)
			}
			rec := core.NewRecord(col)
			rec.Set("token_hash", hashToken(token))
			rec.Set("expires_at", time.Now().Add(tokenTTL).UTC())
			rec.Set("created_by", e.Auth.Id)
			if err := e.App.Save(rec); err != nil {
				return apis.NewInternalServerError("", err)
			}
			return e.JSON(http.StatusOK, map[string]any{
				"token":      token,
				"expires_at": rec.GetDateTime("expires_at"),
			})
		})

		// Redeem a token (unauthenticated — the token IS the credential).
		se.Router.POST("/api/bb/enroll", func(e *core.RequestEvent) error {
			var body struct {
				Token     string `json:"token"`
				PublicKey string `json:"public_key"` // base64 Ed25519
				Name      string `json:"name"`
			}
			if err := e.BindBody(&body); err != nil {
				return apis.NewBadRequestError("invalid body", err)
			}
			body.Token = strings.TrimSpace(body.Token)
			if body.Token == "" || body.PublicKey == "" {
				return apis.NewBadRequestError("token and public_key are required", nil)
			}
			pub, err := base64.StdEncoding.DecodeString(body.PublicKey)
			if err != nil || len(pub) != ed25519.PublicKeySize {
				return apis.NewBadRequestError("public_key must be a base64 Ed25519 public key", nil)
			}

			tokRec, err := e.App.FindFirstRecordByData("enroll_tokens", "token_hash", hashToken(body.Token))
			if err != nil {
				return apis.NewUnauthorizedError("invalid enrollment token", nil)
			}
			if tokRec.GetString("used_at") != "" {
				return apis.NewUnauthorizedError("enrollment token already used", nil)
			}
			if tokRec.GetDateTime("expires_at").Time().Before(time.Now()) {
				return apis.NewUnauthorizedError("enrollment token expired", nil)
			}

			name := body.Name
			if name == "" {
				name = "system-" + hex.EncodeToString(pub[:4])
			}

			sysCol, err := e.App.FindCollectionByNameOrId("systems")
			if err != nil {
				return apis.NewInternalServerError("", err)
			}
			sys := core.NewRecord(sysCol)
			sys.Set("name", name)
			sys.Set("public_key", body.PublicKey)
			sys.Set("status", "offline")

			err = e.App.RunInTransaction(func(tx core.App) error {
				if err := tx.Save(sys); err != nil {
					return err
				}
				tokRec.Set("used_at", time.Now().UTC())
				return tx.Save(tokRec)
			})
			if err != nil {
				return apis.NewInternalServerError("enrollment failed", err)
			}
			return e.JSON(http.StatusOK, map[string]any{"system_id": sys.Id, "name": name})
		})

		return se.Next()
	})
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
