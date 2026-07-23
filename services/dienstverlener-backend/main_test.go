package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	var outwayBody string
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outwayHits++
		if r.URL.Path != "/bri/graphql" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		outwayBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"bsn":"123456789","heeftBelastingjaarAangifte":[{"belastingjaar":2024,"verzamelinkomen":{"waarde":45000,"valuta":"EUR"}}]}}}`))
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
	if !strings.Contains(string(out.Data), "ingeschrevenPersoon") {
		t.Fatalf("data payload missing expected field: %s", out.Data)
	}
	// The request asked for belastingjaren [2024] — the year filter must
	// travel inside the query so the PDP can enforce per-year consent.
	if !strings.Contains(outwayBody, "heeftBelastingjaarAangifte(belastingjaren: [2024])") {
		t.Fatalf("query missing belastingjaren filter: %s", outwayBody)
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

func TestDvtpQueryTimesOutWhenOutwayDoesNotRespond(t *testing.T) {
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pi":"PI-abc123","status":"ACTIVE"}`))
	}))
	defer consent.Close()

	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer outway.Close()

	cfg := config{
		OutwayURL:  outway.URL,
		OutwayPath: "/bri/graphql",
		ConsentURL: consent.URL,
		HTTPClient: &http.Client{Timeout: 25 * time.Millisecond},
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	body := `{"consent_id":"c-1"}`
	resp, err := http.Post(srv.URL+"/api/dvtp/query", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		responseBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 502; body=%s", resp.StatusCode, responseBody)
	}

	var response queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(response.Reason, "fsc_outway_call_failed:") {
		t.Fatalf("reason = %q, want fsc_outway_call_failed prefix", response.Reason)
	}
}
