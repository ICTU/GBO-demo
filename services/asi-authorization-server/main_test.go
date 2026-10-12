package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v4"
)

// The client registration in configuration: every client has an id, a
// redirect_uri and public keys, and only the scopes verify and retrieve.
func TestLoadClients(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := jose.JSONWebKey{Key: &key.PublicKey, KeyID: "k"}
	private := jose.JSONWebKey{Key: key, KeyID: "k"}
	clientDoc := func(edit func(map[string]any)) map[string]any {
		c := map[string]any{"client_id": "qtsp", "redirect_uris": []string{"https://qtsp.test/cb"}, "scope": "verify retrieve",
			"jwks": map[string]any{"keys": []any{public}}}
		if edit != nil {
			edit(c)
		}
		return c
	}
	load := func(t *testing.T, c map[string]any) (map[string]*client, error) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"clients": []any{c}})
		path := filepath.Join(t.TempDir(), "clients.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return loadClients(path)
	}
	clients, err := load(t, clientDoc(nil))
	if err != nil || clients["qtsp"] == nil || !clients["qtsp"].Scopes["verify"] || !clients["qtsp"].Scopes["retrieve"] {
		t.Fatalf("clients = %v, %v; want the client with verify and retrieve", clients, err)
	}
	for name, edit := range map[string]func(map[string]any){
		"no client_id":                 func(c map[string]any) { delete(c, "client_id") },
		"no redirect_uris":             func(c map[string]any) { delete(c, "redirect_uris") },
		"no keys":                      func(c map[string]any) { c["jwks"] = map[string]any{"keys": []any{}} },
		"a private key":                func(c map[string]any) { c["jwks"] = map[string]any{"keys": []any{private}} },
		"a scope that is no operation": func(c map[string]any) { c["scope"] = "verify admin" },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(t, clientDoc(edit)); err == nil || !strings.Contains(err.Error(), "client") {
				t.Fatalf("err = %v, want the registration refused", err)
			}
		})
	}
}
