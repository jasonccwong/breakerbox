// Package identity manages the agent's Ed25519 keypair. The private key is
// generated on this host at enrollment and never leaves it.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

const keyFile = "agent_ed25519.key"

// LoadOrCreate returns the host's private key, generating and persisting a
// new one (0600) on first use.
func LoadOrCreate(stateDir string) (ed25519.PrivateKey, error) {
	path := filepath.Join(stateDir, keyFile)
	if b, err := os.ReadFile(path); err == nil {
		raw, err := base64.StdEncoding.DecodeString(string(b))
		if err != nil || len(raw) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("corrupt key file %s", path)
		}
		return ed25519.PrivateKey(raw), nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(priv)
	if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
		return nil, err
	}
	return priv, nil
}

// PublicKeyB64 returns the base64 public key for a private key.
func PublicKeyB64(priv ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
}
