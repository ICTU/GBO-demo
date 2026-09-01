package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConsentToken(consentID string, scopes []string) string {
	encode := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(map[string]any{
		"consent_id": consentID,
		"pi":         "PI-abc123",
		"scopes":     scopes,
	})
	return encode([]byte(`{"alg":"none"}`)) + "." + encode(payload) + ".sig"
}

func testQueryBody(consentID string, scopes []string, extra map[string]any) string {
	body := map[string]any{"consent_token": testConsentToken(consentID, scopes)}
	for key, value := range extra {
		body[key] = value
	}
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

// Happy-path integration test: the backend reads the token payload to build
// the query and forwards the untouched token to FSC, where the PDP verifies
// it, then returns {allowed:true, data:...}.
func TestDvtpQueryHappyPath(t *testing.T) {
	// Stub FSC-Outway: mirror back a GraphQL-style success payload.
	var outwayHits int
	var outwayBody string
	var forwardedToken string
	var legacyConsentID string
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outwayHits++
		if r.URL.Path != "/bri/graphql" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		outwayBody = string(b)
		forwardedToken = r.Header.Get("X-GBO-Consent-Token")
		legacyConsentID = r.Header.Get("X-GBO-Consent-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"bsn":"123456789","heeftBelastingjaarAangifte":[{"belastingjaar":2024,"verzamelinkomen":{"waarde":45000,"valuta":"EUR"}}]}}}`))
	}))
	defer outway.Close()

	cfg := config{
		Port:       "0",
		OrgSector:  "hypotheekverlener",
		OutwayURL:  outway.URL,
		OutwayPath: "/bri/graphql",
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	token := testConsentToken("c-1", []string{"bd:ib:2024", "bd:ib:2025"})
	bodyBytes, _ := json.Marshal(map[string]any{
		"consent_token":  token,
		"scope_id":       "bd:ib:2025",
		"belastingjaren": []int{2024},
	})
	body := string(bodyBytes)
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
	if forwardedToken != token {
		t.Fatalf("forwarded consent token differs from issued token")
	}
	if legacyConsentID != "" {
		t.Fatalf("legacy X-GBO-Consent-Id header was sent: %q", legacyConsentID)
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
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer outway.Close()

	cfg := config{
		OutwayURL:  outway.URL,
		OutwayPath: "/bri/graphql",
		HTTPClient: &http.Client{Timeout: 25 * time.Millisecond},
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	body := testQueryBody("c-1", []string{"bd:ib:2024", "bd:ib:2025"}, nil)
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
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	body := testQueryBody("c-1", []string{"bd:ib:2025"}, map[string]any{"scope_id": "bd:ib:2025", "belastingjaren": []int{2024, 2025}})
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

// No overlap still reaches FSC/PDP so token verification and revocation cannot
// be bypassed by a local empty-result shortcut.
func TestDvtpQueryNoConsentedYears(t *testing.T) {
	var outwayHits int
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outwayHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer outway.Close()

	cfg := config{
		OutwayURL:  outway.URL,
		OutwayPath: "/bri/graphql",
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	body := testQueryBody("c-1", []string{"bd:ib:2023"}, map[string]any{"scope_id": "bd:ib:2025", "belastingjaren": []int{2024, 2025}})
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
	if outwayHits != 1 {
		t.Fatalf("expected request to reach PDP, got %d outway calls", outwayHits)
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
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/dvtp/query",
		strings.NewReader(testQueryBody("c-1", []string{"bd:ib:2025"}, map[string]any{"scope_id": "bd:ib:2025", "belastingjaren": []int{2024, 2025}})))
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

// A revoked consent must still reach the PDP: the DENY has to be a policy
// decision (CONSENT_WITHDRAWN, with a decision-log entry), not a local
// short-circuit that the portal can only show as a technical error.
func TestDvtpQueryRevokedConsentReachesPDP(t *testing.T) {
	var outwayHits int
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outwayHits++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"allowed":false,"reason":"denied by policy: CONSENT_WITHDRAWN"}`))
	}))
	defer outway.Close()

	cfg := config{
		OutwayURL:  outway.URL,
		OutwayPath: "/bri/graphql",
	}
	srv := httptest.NewServer(newMux(cfg))
	defer srv.Close()

	body := testQueryBody("c-revoked", []string{"bd:ib:2025"}, map[string]any{"scope_id": "bd:ib:2025", "belastingjaren": []int{2025}})
	resp, err := http.Post(srv.URL+"/api/dvtp/query", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var out queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if outwayHits != 1 {
		t.Fatalf("expected the query to reach FSC/PDP, outway hits = %d", outwayHits)
	}
	if out.Allowed {
		t.Fatalf("expected deny, got %+v", out)
	}
	if !strings.Contains(out.Reason, "CONSENT_WITHDRAWN") {
		t.Fatalf("reason = %q, want the policy reason", out.Reason)
	}
	if out.FscTransactionID == "" {
		t.Fatal("expected fsc_transaction_id on the response for decision-log lookup")
	}
}

// The best-effort history post must stay attached to the run that produced
// it. It used to be built with a context-less http.NewRequest and never had
// the propagator injected, so it reached the dev-portal with no traceparent
// and appeared as an orphan trace.
func TestUseHistoryPostCarriesTheTrace(t *testing.T) {
	type captured struct {
		traceparent string
		body        map[string]any
	}
	got := make(chan captured, 1)
	devPortal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history" {
			t.Errorf("path = %q, want /history", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- captured{traceparent: r.Header.Get("Traceparent"), body: body}
		w.WriteHeader(http.StatusOK)
	}))
	defer devPortal.Close()

	// A real provider so the span context is valid and sampled; TraceContext
	// is the propagator the collector setup uses.
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	ctx, span := tp.Tracer("test").Start(context.Background(), "dvtp.query")
	traceID := span.SpanContext().TraceID().String()

	// Exactly how the handler calls it: detached from cancellation, still
	// carrying the trace.
	postUseHistory(context.WithoutCancel(ctx), devPortal.URL, "c-1",
		queryRequest{ConsentToken: "secret-consent-token", ScopeID: "bd:ib:2025"},
		queryResponse{Allowed: true, TraceID: traceID}, traceID)
	span.End()

	select {
	case c := <-got:
		if c.traceparent == "" {
			t.Fatal("history post carried no traceparent: it is detached from the run's trace")
		}
		if !strings.Contains(c.traceparent, traceID) {
			t.Errorf("traceparent = %q, want it to carry trace id %s", c.traceparent, traceID)
		}
		if c.body["trace_id"] != traceID {
			t.Errorf("body trace_id = %v, want %s", c.body["trace_id"], traceID)
		}
		encoded, _ := json.Marshal(c.body)
		if strings.Contains(string(encoded), "secret-consent-token") {
			t.Fatalf("history payload leaked consent token: %s", encoded)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dev-portal never received the history post")
	}
}

// The post must survive the handler returning: WithoutCancel keeps the trace
// while dropping the request's cancellation.
func TestUseHistoryPostSurvivesHandlerReturn(t *testing.T) {
	done := make(chan struct{}, 1)
	devPortal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		done <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer devPortal.Close()

	handlerCtx, cancel := context.WithCancel(context.Background())
	postCtx := context.WithoutCancel(handlerCtx)
	cancel() // the handler returns before the goroutine runs

	postUseHistory(postCtx, devPortal.URL, "c-1", queryRequest{}, queryResponse{Allowed: true}, "")

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("post was cancelled with the handler; it must outlive it")
	}
}
