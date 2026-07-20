package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Happy-path integration test: two stub upstreams (consent-register +
// FSC-Outway) return canned responses; the backend chains consent-lookup →
// outway POST and returns {allowed:true, data:...}. Covers the full
// wiring — handleQuery → fetchConsentPI → outway roundtrip → JSON assembly.
func TestDvtpQueryHappyPath(t *testing.T) {
	// Stub consent-register: return an ACTIVE consent with a PI.
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/consents/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pi":"PI-abc123","status":"ACTIVE"}`))
	}))
	defer consent.Close()

	// Stub FSC-Outway: mirror back a GraphQL-style success payload.
	var outwayHits int
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outwayHits++
		if r.URL.Path != "/bri/graphql" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"inkomensgegevens":[{"belastingjaar":2024,"verzamelinkomen":45000}]}}`))
	}))
	defer outway.Close()

	cfg := config{
		Port:       "0",
		OrgOIN:     "99999999900000000300",
		OrgSector:  "hypotheekverlener",
		OutwayURL:  outway.URL,
		OutwayPath: "/bri/graphql",
		ConsentURL: consent.URL,
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	body := `{"consent_id":"c-1","scope_id":"bd:ib:2025","belastingjaren":[2024]}`
	resp, err := http.Post(srv.URL+"/api/dvtp/query", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, b)
	}

	var out struct {
		Allowed bool            `json:"allowed"`
		Data    json.RawMessage `json:"data"`
		Reason  string          `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Allowed {
		t.Fatalf("expected allowed=true, got %+v", out)
	}
	if outwayHits != 1 {
		t.Fatalf("expected 1 outway hit, got %d", outwayHits)
	}
	if !strings.Contains(string(out.Data), "inkomensgegevens") {
		t.Fatalf("data payload missing expected field: %s", out.Data)
	}
}

func TestDvtpHealth(t *testing.T) {
	srv := httptest.NewServer(newMux(config{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
}
