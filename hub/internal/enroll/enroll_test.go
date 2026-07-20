package enroll

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"

	_ "github.com/breakerbox/breakerbox/hub/migrations"
)

func testServer(t *testing.T) (*tests.TestApp, string) {
	t.Helper()
	app, err := tests.NewTestAppWithConfig(core.BaseAppConfig{DataDir: t.TempDir(), EncryptionEnv: "pb_test_env"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	Register(app)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	go func() { _ = apis.Serve(app, apis.ServeConfig{HttpAddr: addr, ShowStartBanner: false}) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + addr + "/api/health"); err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return app, addr
}

// mintToken creates an enroll token directly (bypassing the authed route,
// which is exercised separately via its auth rejection).
func mintToken(t *testing.T, app core.App, expired bool) string {
	t.Helper()
	raw := make([]byte, 24)
	rand.Read(raw)
	token := "bbe_" + base64.RawURLEncoding.EncodeToString(raw)
	col, err := app.FindCollectionByNameOrId("enroll_tokens")
	if err != nil {
		t.Fatal(err)
	}
	rec := core.NewRecord(col)
	rec.Set("token_hash", hashToken(token))
	exp := time.Now().Add(30 * time.Minute)
	if expired {
		exp = time.Now().Add(-1 * time.Minute)
	}
	rec.Set("expires_at", exp.UTC())
	if err := app.Save(rec); err != nil {
		t.Fatal(err)
	}
	return token
}

func postEnroll(t *testing.T, addr, token, pubKey string) (*http.Response, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"token": token, "public_key": pubKey, "name": "test-host"})
	resp, err := http.Post("http://"+addr+"/api/bb/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestEnrollFlow(t *testing.T) {
	app, addr := testServer(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	// Happy path.
	token := mintToken(t, app, false)
	resp, out := postEnroll(t, addr, token, pubB64)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll failed: %d %v", resp.StatusCode, out)
	}
	systemID, _ := out["system_id"].(string)
	if systemID == "" {
		t.Fatal("no system_id returned")
	}
	sys, err := app.FindRecordById("systems", systemID)
	if err != nil {
		t.Fatal(err)
	}
	if sys.GetString("public_key") != pubB64 || sys.GetString("name") != "test-host" {
		t.Errorf("system record wrong: pk=%q name=%q", sys.GetString("public_key"), sys.GetString("name"))
	}

	// Token reuse must fail.
	if resp, _ := postEnroll(t, addr, token, pubB64); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("reused token accepted: %d", resp.StatusCode)
	}
	// Expired token must fail.
	if resp, _ := postEnroll(t, addr, mintToken(t, app, true), pubB64); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expired token accepted: %d", resp.StatusCode)
	}
	// Garbage token must fail.
	if resp, _ := postEnroll(t, addr, "bbe_nope", pubB64); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("garbage token accepted: %d", resp.StatusCode)
	}
	// Bad public key must fail.
	if resp, _ := postEnroll(t, addr, mintToken(t, app, false), "not-a-key"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad public key accepted: %d", resp.StatusCode)
	}
}

func TestMintRequiresAuth(t *testing.T) {
	_, addr := testServer(t)
	resp, err := http.Post(fmt.Sprintf("http://%s/api/bb/enroll-tokens", addr), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated mint returned %d, want 401", resp.StatusCode)
	}
}
