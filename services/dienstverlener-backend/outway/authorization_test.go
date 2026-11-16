package outway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/dienstverlener-backend/consentrequest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type received struct {
	path   string
	header http.Header
	form   url.Values
}

func consentPortal(t *testing.T, status int, answer string) (*ConsentPortal, *received) {
	t.Helper()
	got := &received{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got.path, got.header, got.form = r.URL.Path, r.Header.Clone(), r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	return &ConsentPortal{URL: srv.URL + "/consent-authorization", Client: &http.Client{Timeout: 2 * time.Second}}, got
}

func tracedContext(t *testing.T) (context.Context, string) {
	t.Helper()
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	traceID, _ := trace.TraceIDFromHex("0190c5a27b1e7cc08f6e1a2b3c4d5e6f")
	spanID, _ := trace.SpanIDFromHex("1a2b3c4d5e6f7081")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled})
	return trace.ContextWithSpanContext(context.Background(), sc), traceID.String()
}

// The pushed request is form-encoded with exactly the PAR parameters; no
// client_name or purpose. The trace travels along.
func TestThePushedRequestReachesTheOutway(t *testing.T) {
	portal, got := consentPortal(t, http.StatusCreated, `{"request_uri":"urn:ietf:params:oauth:request_uri:r1","expires_in":600}`)
	ctx, traceID := tracedContext(t)

	requestURI, err := portal.Push(ctx, consentrequest.PushedRequest{
		ClientID: "99999999900000000300", Service: "aangiftegegevens", Scopes: []string{"bd:ib:2025", "bd:ib:2024"},
		RedirectURI: "http://localhost:9001/return", State: "st", CodeChallenge: "ch", ValidUntil: "2027-01-01T00:00:00Z",
	})
	if err != nil || requestURI != "urn:ietf:params:oauth:request_uri:r1" {
		t.Fatalf("push: %q, %v", requestURI, err)
	}
	if got.path != "/consent-authorization/par" || !strings.HasPrefix(got.header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		t.Errorf("path %s, content type %s", got.path, got.header.Get("Content-Type"))
	}
	want := url.Values{
		"response_type": {"code"}, "client_id": {"99999999900000000300"}, "service": {"aangiftegegevens"},
		"scope": {"bd:ib:2025 bd:ib:2024"}, "redirect_uri": {"http://localhost:9001/return"}, "state": {"st"},
		"code_challenge": {"ch"}, "code_challenge_method": {"S256"}, "valid_until": {"2027-01-01T00:00:00Z"},
	}
	if got.form.Encode() != want.Encode() {
		t.Errorf("form %v, want %v", got.form, want)
	}
	if tp := got.header.Get("Traceparent"); !strings.Contains(tp, traceID) {
		t.Errorf("traceparent %q, want trace %s", tp, traceID)
	}
}

func TestAPushWithoutValidUntilOmitsIt(t *testing.T) {
	portal, got := consentPortal(t, http.StatusCreated, `{"request_uri":"urn:x"}`)
	if _, err := portal.Push(context.Background(), consentrequest.PushedRequest{ClientID: "c", Scopes: []string{"s"}}); err != nil {
		t.Fatal(err)
	}
	if got.form.Has("valid_until") {
		t.Error("sent an empty valid_until")
	}
}

func TestAnOAuthErrorIsARefusal(t *testing.T) {
	portal, _ := consentPortal(t, http.StatusBadRequest, `{"error":"invalid_scope","error_description":"unknown","reason":"UNKNOWN_SCOPE"}`)
	_, err := portal.Push(context.Background(), consentrequest.PushedRequest{})
	var refusal *consentrequest.Refusal
	if !errors.As(err, &refusal) || refusal.Code != "invalid_scope" || refusal.Reason != "UNKNOWN_SCOPE" || refusal.Description != "unknown" {
		t.Fatalf("err = %#v", err)
	}
}

// An Outway that cannot reach the portal answers in its own format; that is
// not a refusal by the portal.
func TestAnOutwayErrorIsNotARefusal(t *testing.T) {
	portal, _ := consentPortal(t, http.StatusBadGateway, `{"message":"no valid contract"}`)
	_, err := portal.Push(context.Background(), consentrequest.PushedRequest{})
	var refusal *consentrequest.Refusal
	if err == nil || errors.As(err, &refusal) || !strings.Contains(err.Error(), "no valid contract") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheCodeIsExchangedForTheToken(t *testing.T) {
	portal, got := consentPortal(t, http.StatusOK, `{"access_token":"a.b.c","token_type":"Bearer","scope":"bd:ib:2025 bd:ib:2024","consent_id":"c-1"}`)
	ctx, traceID := tracedContext(t)

	consent, err := portal.Exchange(ctx, consentrequest.CodeExchange{
		ClientID: "99999999900000000300", Code: "code-1", RedirectURI: "http://localhost:9001/return", CodeVerifier: "ver",
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if consent.Token != "a.b.c" || consent.ConsentID != "c-1" || strings.Join(consent.Scopes, ",") != "bd:ib:2025,bd:ib:2024" {
		t.Errorf("consent %+v", consent)
	}
	want := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {"99999999900000000300"}, "code": {"code-1"},
		"redirect_uri": {"http://localhost:9001/return"}, "code_verifier": {"ver"},
	}
	if got.path != "/consent-authorization/token" || got.form.Encode() != want.Encode() {
		t.Errorf("path %s, form %v", got.path, got.form)
	}
	if tp := got.header.Get("Traceparent"); !strings.Contains(tp, traceID) {
		t.Errorf("traceparent %q, want trace %s", tp, traceID)
	}
}

func TestARefusedExchangeIsARefusal(t *testing.T) {
	portal, _ := consentPortal(t, http.StatusBadRequest, `{"error":"invalid_grant","error_description":"the code was used"}`)
	_, err := portal.Exchange(context.Background(), consentrequest.CodeExchange{})
	var refusal *consentrequest.Refusal
	if !errors.As(err, &refusal) || refusal.Code != "invalid_grant" {
		t.Fatalf("err = %v", err)
	}
}
