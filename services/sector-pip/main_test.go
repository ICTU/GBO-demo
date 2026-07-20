package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
