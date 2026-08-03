package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Happy-path integration test: look up a known OIN and verify the
// sector/name are returned. Uses an in-memory fixture instead of the
// on-disk organizations.json so the test does not depend on config layout.
func TestLookupKnownOIN(t *testing.T) {
	orgs := map[string]Organization{
		"00000001234567890000": {
			OIN:      "00000001234567890000",
			Name:     "Demo Hypotheekverlener BV",
			Sector:   "hypotheekverlener",
			KVKSBI:   "6492",
			Register: "KvK",
		},
	}
	srv := httptest.NewServer(newMux(orgs))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/organizations/00000001234567890000")
	if err != nil {
		t.Fatalf("get organization: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Valid  bool   `json:"valid"`
		OIN    string `json:"oin"`
		Name   string `json:"name"`
		Sector string `json:"sector"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Valid {
		t.Fatalf("valid = false, want true: %+v", out)
	}
	if out.OIN != "00000001234567890000" {
		t.Fatalf("OIN mismatch: got %q", out.OIN)
	}
	if out.Sector != "hypotheekverlener" {
		t.Fatalf("sector = %q, want %q", out.Sector, "hypotheekverlener")
	}
}

// loadOrganizations returns errors instead of exiting, so these paths are
// reachable from a test at all — previously a bad file called os.Exit(1).
func TestLoadOrganizations(t *testing.T) {
	dir := t.TempDir()

	valid := filepath.Join(dir, "ok.json")
	if err := os.WriteFile(valid, []byte(`[{"oin":"OIN-1","name":"Bron A","sector":"belastingen"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	orgs, err := loadOrganizations(valid)
	if err != nil {
		t.Fatalf("valid register: %v", err)
	}
	if len(orgs) != 1 || orgs["OIN-1"].Name != "Bron A" {
		t.Errorf("orgs = %+v, want one entry keyed by OIN", orgs)
	}

	// A missing register is fatal on purpose: serving an empty one answers
	// "OIN not found" for everything while still passing the health check.
	if _, err := loadOrganizations(filepath.Join(dir, "nope.json")); err == nil {
		t.Error("missing register: want an error, got nil")
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrganizations(bad); err == nil {
		t.Error("malformed register: want an error, got nil")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("PIP_CONFIG_PATH", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// 4004 is what the container exposes and the compose healthcheck probes.
	if cfg.Port != "4004" {
		t.Errorf("Port = %q, want 4004", cfg.Port)
	}
	if cfg.ConfigPath != "/config/organizations.json" {
		t.Errorf("ConfigPath = %q, want the mounted default", cfg.ConfigPath)
	}

	t.Setenv("PORT", "9999")
	cfg, _ = loadConfig()
	if cfg.Port != "9999" {
		t.Errorf("PORT override ignored: got %q", cfg.Port)
	}
}
