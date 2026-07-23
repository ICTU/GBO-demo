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
	// Stub consent-register: return an ACTIVE consent with a PI and the
	// scopes the test requests years for (the backend intersects requested
	// years with consented scopes before querying).
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/consents/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pi":"PI-abc123","status":"ACTIVE","scopes":["bd:ib:2024","bd:ib:2025"]}`))
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
		_, _ = w.Write([]byte(`{"pi":"PI-abc123","status":"ACTIVE","scopes":["bd:ib:2024","bd:ib:2025"]}`))
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

// Partial consent: the consent covers 2025 only; a request for
// [2024, 2025] must query only 2025 and report 2024 as denied — instead
// of letting the whole query fail policy (YEAR_NOT_COVERED).
func TestDvtpQueryIntersectsConsentedYears(t *testing.T) {
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pi":"PI-abc123","status":"ACTIVE","scopes":["bd:ib:2025"]}`))
	}))
	defer consent.Close()

	var outwayHits int
	var outwayBody string
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outwayHits++
		b, _ := io.ReadAll(r.Body)
		outwayBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"heeftBelastingjaarAangifte":[{"belastingjaar":2025}]}}}`))
	}))
	defer outway.Close()

	cfg := config{
		OutwayURL:  outway.URL,
		OutwayPath: "/bri/graphql",
		ConsentURL: consent.URL,
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	body := `{"consent_id":"c-1","scope_id":"bd:ib:2025","belastingjaren":[2024,2025]}`
	resp, err := http.Post(srv.URL+"/api/dvtp/query", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var out queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Allowed {
		t.Fatalf("expected allowed=true, got %+v", out)
	}
	if !strings.Contains(outwayBody, "belastingjaren: [2025]") {
		t.Fatalf("expected query filtered to [2025], got: %s", outwayBody)
	}
	if strings.Contains(outwayBody, "2024") {
		t.Fatalf("expected 2024 absent from query, got: %s", outwayBody)
	}
	if len(out.DeniedYears) != 1 || out.DeniedYears[0] != 2024 {
		t.Fatalf("denied_years = %v, want [2024]", out.DeniedYears)
	}
}

// No overlap between requested years and consented scopes: the FSC call
// is skipped and every requested year is reported denied.
func TestDvtpQueryNoConsentedYears(t *testing.T) {
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pi":"PI-abc123","status":"ACTIVE","scopes":["bd:ib:2023"]}`))
	}))
	defer consent.Close()

	var outwayHits int
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outwayHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer outway.Close()

	cfg := config{
		OutwayURL:  outway.URL,
		OutwayPath: "/bri/graphql",
		ConsentURL: consent.URL,
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	body := `{"consent_id":"c-1","scope_id":"bd:ib:2025","belastingjaren":[2024,2025]}`
	resp, err := http.Post(srv.URL+"/api/dvtp/query", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var out queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Allowed {
		t.Fatalf("expected allowed=true, got %+v", out)
	}
	if outwayHits != 0 {
		t.Fatalf("expected no outway call, got %d", outwayHits)
	}
	if len(out.DeniedYears) != 2 {
		t.Fatalf("denied_years = %v, want [2024 2025]", out.DeniedYears)
	}
}

// Dev-portal requests (X-Demo-Source: dev-portal) bypass the year
// intersection: the portal demonstrates raw policy outcomes, so the
// query must carry the requested years verbatim — letting the PDP deny
// unconsented years (YEAR_NOT_COVERED) with a full trace.
func TestDvtpQueryDevPortalBypassesIntersection(t *testing.T) {
	consent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"pi":"PI-abc123","status":"ACTIVE","scopes":["bd:ib:2025"]}`))
	}))
	defer consent.Close()

	var outwayBody string
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		outwayBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"allowed":false,"reason":"denied by policy: YEAR_NOT_COVERED"}`))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer outway.Close()

	cfg := config{
		OutwayURL:  outway.URL,
		OutwayPath: "/bri/graphql",
		ConsentURL: consent.URL,
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/dvtp/query",
		strings.NewReader(`{"consent_id":"c-1","scope_id":"bd:ib:2025","belastingjaren":[2024,2025]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Demo-Source", "dev-portal")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if !strings.Contains(outwayBody, "belastingjaren: [2024,2025]") {
		t.Fatalf("expected query with years verbatim, got: %s", outwayBody)
	}
}
