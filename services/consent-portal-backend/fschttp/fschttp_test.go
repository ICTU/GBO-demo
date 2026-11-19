package fschttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/consent-portal-backend/consent"
)

const (
	hypotheekPeer = "99999999900000000300"
	incassoPeer   = "99999999900000000500"
	returnURL     = "http://localhost:9001/return"
)

type fakeRegistrations struct{ err error }

func (f fakeRegistrations) Registration(_ context.Context, peer consent.PeerID) (consent.Registration, error) {
	if f.err != nil {
		return consent.Registration{}, f.err
	}
	switch peer {
	case hypotheekPeer:
		return consent.Registration{
			Name:            "Demo Hypotheekverlener BV",
			Active:          true,
			Services:        []consent.RegisteredService{{Service: "aangiftegegevens", Purpose: "Hypotheek-aanvraag"}},
			ReturnLocations: []string{returnURL},
		}, nil
	case incassoPeer:
		return consent.Registration{Name: "Demo Incassobureau BV"}, nil
	}
	return consent.Registration{}, consent.ErrUnknownParty
}

func newServer(t *testing.T, registrations consent.Registrations, now time.Time) (*httptest.Server, *consent.Portal) {
	t.Helper()
	portal := &consent.Portal{
		Registrations: registrations,
		KnownScopes:   []string{"bd:ib:2025", "bd:ib:2024"},
		Requests:      consent.NewRequestStore(10 * time.Minute),
		Now:           func() time.Time { return now },
	}
	server := httptest.NewServer(NewMux(portal))
	t.Cleanup(server.Close)
	return server, portal
}

func validForm() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {hypotheekPeer},
		"service":               {"aangiftegegevens"},
		"scope":                 {"bd:ib:2025 bd:ib:2024"},
		"redirect_uri":          {returnURL},
		"state":                 {"af0ifjsldkj"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
	}
}

// fscToken is an Fsc-Authorization value with peer as its subject. The
// Inway has verified the token; this listener only reads it.
func fscToken(peer string) string {
	claims, _ := json.Marshal(map[string]string{"sub": peer})
	return "Bearer e30." + base64.RawURLEncoding.EncodeToString(claims) + ".c2ln"
}

func push(t *testing.T, server *httptest.Server, peer string, form url.Values) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/par", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if peer != "" {
		req.Header.Set("Fsc-Authorization", fscToken(peer))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
	return resp, body
}

func TestPARAcceptsARegisteredRequest(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	server, portal := newServer(t, fakeRegistrations{}, now)

	resp, body := push(t, server, hypotheekPeer, validForm())
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body %v", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	uri, _ := body["request_uri"].(string)
	if !strings.HasPrefix(uri, "urn:ietf:params:oauth:request_uri:") {
		t.Errorf("request_uri = %v", body["request_uri"])
	}
	if body["expires_in"] != float64(600) {
		t.Errorf("expires_in = %v, want 600", body["expires_in"])
	}
	pending, found := portal.Requests.Get(uri, now)
	if !found {
		t.Fatal("the request is not kept")
	}
	if got := pending.Request.Scopes; len(got) != 2 || got[0] != "bd:ib:2025" || got[1] != "bd:ib:2024" {
		t.Errorf("scopes = %v, want the space-separated scope split", got)
	}
}

func TestPARRefusesWithTheReasonCode(t *testing.T) {
	cases := []struct {
		name          string
		peer          string
		change        func(url.Values)
		registrations consent.Registrations
		status        int
		error         string
		reason        consent.ReasonCode
	}{
		{"no client_id", hypotheekPeer, func(f url.Values) { f.Del("client_id") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"no response_type", hypotheekPeer, func(f url.Values) { f.Del("response_type") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"no state", hypotheekPeer, func(f url.Values) { f.Del("state") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"no code_challenge", hypotheekPeer, func(f url.Values) { f.Del("code_challenge") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"plain challenge method", hypotheekPeer, func(f url.Values) { f.Set("code_challenge_method", "plain") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"no service", hypotheekPeer, func(f url.Values) { f.Del("service") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"valid_until in the past", hypotheekPeer, func(f url.Values) { f.Set("valid_until", "2020-01-01T00:00:00Z") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"repeated state", hypotheekPeer, func(f url.Values) { f.Add("state", "other") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"repeated scope", hypotheekPeer, func(f url.Values) { f.Add("scope", "bd:ib:2025") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"repeated unknown parameter", hypotheekPeer, func(f url.Values) { f["extra"] = []string{"a", "b"} }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"request_uri in the body", hypotheekPeer, func(f url.Values) { f.Set("request_uri", "urn:ietf:params:oauth:request_uri:abc") }, nil, 400, "invalid_request", consent.ReasonInitiationIncomplete},
		{"inactive provider", incassoPeer, func(f url.Values) { f.Set("client_id", incassoPeer) }, nil, 400, "unauthorized_client", consent.ReasonPartyNotActive},
		{"unknown provider", "99999999900000009999", func(f url.Values) { f.Set("client_id", "99999999900000009999") }, nil, 400, "unauthorized_client", consent.ReasonPartyNotActive},
		{"client_id is another peer", hypotheekPeer, func(f url.Values) { f.Set("client_id", incassoPeer) }, nil, 400, "invalid_request", consent.ReasonInitiationMismatch},
		{"unregistered service", hypotheekPeer, func(f url.Values) { f.Set("service", "andere-dienst") }, nil, 400, "invalid_request", consent.ReasonInitiationMismatch},
		{"other purpose", hypotheekPeer, func(f url.Values) { f.Set("purpose", "Iets anders") }, nil, 400, "invalid_request", consent.ReasonInitiationMismatch},
		{"unknown data set", hypotheekPeer, func(f url.Values) { f.Set("scope", "bd:ib:2025 bd:ib:1999") }, nil, 400, "invalid_scope", consent.ReasonUnknownScope},
		{"unregistered return URL", hypotheekPeer, func(f url.Values) { f.Set("redirect_uri", returnURL+"?x=1") }, nil, 400, "invalid_request", consent.ReasonReturnURLNotRegistered},
		{"register unavailable", hypotheekPeer, func(url.Values) {}, fakeRegistrations{err: errors.New("connection refused")}, 503, "temporarily_unavailable", consent.ReasonAdmissionRegisterUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registrations := tc.registrations
			if registrations == nil {
				registrations = fakeRegistrations{}
			}
			server, _ := newServer(t, registrations, time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
			form := validForm()
			tc.change(form)

			resp, body := push(t, server, tc.peer, form)
			if resp.StatusCode != tc.status || body["error"] != tc.error || body["reason"] != string(tc.reason) {
				t.Errorf("got %d %v, want %d %s with reason %s", resp.StatusCode, body, tc.status, tc.error, tc.reason)
			}
			if _, leaked := body["request_uri"]; leaked {
				t.Error("a refused request got a request_uri")
			}
		})
	}
}

func TestPARNeedsAnFSCPeer(t *testing.T) {
	server, _ := newServer(t, fakeRegistrations{}, time.Now())
	for name, header := range map[string]string{
		"no header":          "",
		"no subject":         "Bearer e30.e30.c2ln",
		"not a peer ID":      fscToken("not-a-peer"),
		"not a token at all": "Bearer nonsense",
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, server.URL+"/par", strings.NewReader(validForm().Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if header != "" {
				req.Header.Set("Fsc-Authorization", header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
		})
	}
}

func TestPARReadsTheFormBodyOnly(t *testing.T) {
	server, _ := newServer(t, fakeRegistrations{}, time.Now())

	query := validForm()
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/par?"+query.Encode(), strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Fsc-Authorization", fscToken(hypotheekPeer))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	err = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusBadRequest || body["reason"] != string(consent.ReasonInitiationIncomplete) {
		t.Errorf("parameters in the query: got %d %v (%v), want 400 with reason %s", resp.StatusCode, body, err, consent.ReasonInitiationIncomplete)
	}

	req, _ = http.NewRequest(http.MethodPost, server.URL+"/par", strings.NewReader(fmt.Sprintf(`{"client_id":%q}`, hypotheekPeer)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Fsc-Authorization", fscToken(hypotheekPeer))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a JSON body: status = %d, want 400", resp.StatusCode)
	}
}

func TestPARRefusesAnOversizedBody(t *testing.T) {
	server, _ := newServer(t, fakeRegistrations{}, time.Now())
	form := validForm()
	form.Set("state", strings.Repeat("x", maxFormBytes))

	resp, body := push(t, server, hypotheekPeer, form)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || body["error"] != "invalid_request" {
		t.Errorf("got %d %v, want 413 invalid_request", resp.StatusCode, body)
	}
}

func TestPARWithoutARequestStoreIsAServerError(t *testing.T) {
	portal := &consent.Portal{
		Registrations: fakeRegistrations{},
		KnownScopes:   []string{"bd:ib:2025", "bd:ib:2024"},
	}
	server := httptest.NewServer(NewMux(portal))
	t.Cleanup(server.Close)

	resp, body := push(t, server, hypotheekPeer, validForm())
	if resp.StatusCode != http.StatusInternalServerError || body["error"] != "server_error" {
		t.Errorf("got %d %v, want 500 server_error", resp.StatusCode, body)
	}
	if _, leaked := body["reason"]; leaked {
		t.Errorf("a server error carries a reason: %v", body)
	}
}

func TestPARIsPOSTOnly(t *testing.T) {
	server, _ := newServer(t, fakeRegistrations{}, time.Now())
	resp, err := http.Get(server.URL + "/par")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /par: status = %d, want 405", resp.StatusCode)
	}
}

func TestPARRequestExpires(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	server, portal := newServer(t, fakeRegistrations{}, now)
	_, body := push(t, server, hypotheekPeer, validForm())
	uri, _ := body["request_uri"].(string)

	if _, found := portal.Requests.Get(uri, now.Add(599*time.Second)); !found {
		t.Error("the request expired before expires_in")
	}
	if _, found := portal.Requests.Get(uri, now.Add(600*time.Second)); found {
		t.Error("the request is still there after expires_in")
	}
}
