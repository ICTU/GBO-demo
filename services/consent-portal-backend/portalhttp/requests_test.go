package portalhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gbo-demo/consent-portal-backend/consent"
)

const (
	hypotheekPeer = "99999999900000000300"
	returnURL     = "http://localhost:9001/return"
	citizenA      = "999993653"
	citizenB      = "999990019"
)

// ── Fakes ─────────────────────────────────────────────────────────────────

type fakeRegistrations struct{ returnURL string }

func (f fakeRegistrations) Registration(_ context.Context, peer consent.PeerID) (consent.Registration, error) {
	if peer != hypotheekPeer {
		return consent.Registration{}, consent.ErrUnknownParty
	}
	return consent.Registration{
		Name:            "Demo Hypotheekverlener BV",
		Active:          true,
		Services:        []consent.RegisteredService{{Service: "aangiftegegevens", Purpose: "Hypotheek-aanvraag"}},
		ReturnLocations: []string{f.returnURL},
	}, nil
}

type fakeBSNk struct{}

func (fakeBSNk) Activate(context.Context, consent.BSN) (consent.Polymorphic, error) {
	return consent.Polymorphic{PI: "PI", PP: "PP"}, nil
}

func (fakeBSNk) AuthorisedForBSN(context.Context) ([]string, error) { return nil, nil }

func (fakeBSNk) Transform(_ context.Context, _ consent.Polymorphic, form consent.Form, parties []consent.Party) (map[string]string, error) {
	values := map[string]string{}
	for _, party := range parties {
		values[party.OIN] = string(form) + "-" + party.OIN
	}
	return values, nil
}

// fakeRegister is the consent register: it counts the consents it gives,
// or fails to give one.
type fakeRegister struct {
	mu      sync.Mutex
	created []consent.Draft
	err     error
}

func (f *fakeRegister) Create(_ context.Context, d consent.Draft) (consent.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return consent.Record{}, f.err
	}
	f.created = append(f.created, d)
	return consent.Record{ID: "c-1", Token: "signed-consent-token", Status: "ACTIVE"}, nil
}

func (f *fakeRegister) ListBySubject(context.Context, consent.SubjectRef) ([]consent.Record, error) {
	return nil, nil
}

func (f *fakeRegister) Get(context.Context, string) (consent.Record, error) {
	return consent.Record{}, consent.ErrNotFound
}

func (f *fakeRegister) Revoke(context.Context, string) error { return consent.ErrNotFound }

func (f *fakeRegister) PolymorphicFor(context.Context, consent.SubjectRef) (consent.Polymorphic, error) {
	return consent.Polymorphic{}, consent.ErrNotFound
}

func (f *fakeRegister) KeepPolymorphic(context.Context, consent.SubjectRef, consent.Polymorphic) error {
	return nil
}

func (f *fakeRegister) consents() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

// ── Harness ───────────────────────────────────────────────────────────────

type harness struct {
	server   *httptest.Server
	portal   *consent.Portal
	register *fakeRegister
	now      time.Time
}

func newHarness(t *testing.T, registeredReturnURL string) *harness {
	t.Helper()
	h := &harness{register: &fakeRegister{}, now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	h.portal = &consent.Portal{
		Identities:    fakeBSNk{},
		SubjectRefs:   consent.SubjectRefs{Key: []byte("test-subject-ref-key-of-32-bytes!"), Version: "1"},
		Consents:      h.register,
		Sources:       []consent.Party{{OIN: "00000000000000000200", KeySetVersion: 20260101}},
		Registrations: fakeRegistrations{returnURL: registeredReturnURL},
		KnownScopes:   []string{"bd:ib:2025", "bd:ib:2024", "lvg:vbo:eigendom"},
		Requests:      consent.NewRequestStore(10 * time.Minute),
		Codes:         consent.NewCodeStore(time.Minute),
		Now:           func() time.Time { return h.now },
	}
	h.server = httptest.NewServer(NewMux(h.portal, NewHub()))
	t.Cleanup(h.server.Close)
	return h
}

// push pushes Hypotheek-BV's request and returns its request URI.
func (h *harness) push(t *testing.T, redirectURI string) string {
	t.Helper()
	ref, err := h.portal.PushRequest(context.Background(), hypotheekPeer, consent.PushedRequest{
		ResponseType:        "code",
		ClientID:            hypotheekPeer,
		Service:             "aangiftegegevens",
		Scopes:              []string{"bd:ib:2025", "bd:ib:2024"},
		RedirectURI:         redirectURI,
		State:               "af0ifjsldkj",
		CodeChallenge:       "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		CodeChallengeMethod: "S256",
		ValidUntil:          "2027-02-08T12:00:00Z",
	})
	if err != nil {
		t.Fatalf("PushRequest: %v", err)
	}
	return ref.RequestURI
}

// call sends a request as citizen ("" for none) and returns the status and
// the raw body.
func (h *harness) call(t *testing.T, method, path, citizen string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if citizen != "" {
		token, err := signPortalToken(citizen)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (h *harness) resolve(t *testing.T, uri, clientID, citizen string) (int, map[string]any) {
	t.Helper()
	status, raw := h.call(t, http.MethodGet, "/portal/requests/"+url.PathEscape(uri)+"?client_id="+url.QueryEscape(clientID), citizen, nil)
	return status, decode(t, raw)
}

func (h *harness) decide(t *testing.T, uri, citizen string, body any) (int, []byte) {
	t.Helper()
	return h.call(t, http.MethodPost, "/portal/requests/"+url.PathEscape(uri)+"/decision", citizen, body)
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decoding %q: %v", raw, err)
	}
	return body
}

var grantBoth = map[string]any{"decision": "grant", "scopes": []string{"bd:ib:2025", "bd:ib:2024"}}

// ── Resolve ───────────────────────────────────────────────────────────────

func TestResolveShowsTheRegistration(t *testing.T) {
	h := newHarness(t, returnURL)
	uri := h.push(t, returnURL)

	status, body := h.resolve(t, uri, hypotheekPeer, citizenA)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %v", status, body)
	}
	if body["client_id"] != hypotheekPeer || body["client_name"] != "Demo Hypotheekverlener BV" ||
		body["service"] != "aangiftegegevens" || body["purpose"] != "Hypotheek-aanvraag" ||
		body["valid_until"] != "2027-02-08T12:00:00Z" {
		t.Errorf("body = %v", body)
	}
	if scopes, _ := body["scopes"].([]any); len(scopes) != 2 {
		t.Errorf("scopes = %v", body["scopes"])
	}
	for _, hidden := range []string{"redirect_uri", "state", "code_challenge"} {
		if _, shown := body[hidden]; shown {
			t.Errorf("resolve shows %s: %v", hidden, body)
		}
	}

	if status, body := h.resolve(t, uri, hypotheekPeer, citizenA); status != http.StatusOK {
		t.Errorf("reload by the same citizen: %d %v", status, body)
	}
}

func TestResolveRefusesWithTheReasonCode(t *testing.T) {
	cases := []struct {
		name     string
		prepare  func(t *testing.T, h *harness, uri string) (ref, clientID, citizen string)
		status   int
		reason   consent.ReasonCode
		withBody bool
	}{
		{"not logged in", func(_ *testing.T, _ *harness, uri string) (string, string, string) {
			return uri, hypotheekPeer, ""
		}, 401, "", false},
		{"unknown reference", func(_ *testing.T, _ *harness, _ string) (string, string, string) {
			return "urn:ietf:params:oauth:request_uri:unknown", hypotheekPeer, citizenA
		}, 404, consent.ReasonRequestURIUnknown, true},
		{"expired reference", func(_ *testing.T, h *harness, uri string) (string, string, string) {
			h.now = h.now.Add(10 * time.Minute)
			return uri, hypotheekPeer, citizenA
		}, 410, consent.ReasonRequestURIExpired, true},
		{"spent reference", func(t *testing.T, h *harness, uri string) (string, string, string) {
			if status, raw := h.decide(t, uri, citizenA, map[string]any{"decision": "deny"}); status != http.StatusOK {
				t.Fatalf("deny: %d %s", status, raw)
			}
			return uri, hypotheekPeer, citizenA
		}, 410, consent.ReasonRequestURISpent, true},
		{"other client_id", func(_ *testing.T, _ *harness, uri string) (string, string, string) {
			return uri, "99999999900000001000", citizenA
		}, 400, consent.ReasonClientIDMismatch, true},
		{"other citizen", func(t *testing.T, h *harness, uri string) (string, string, string) {
			if status, body := h.resolve(t, uri, hypotheekPeer, citizenA); status != http.StatusOK {
				t.Fatalf("first resolve: %d %v", status, body)
			}
			return uri, hypotheekPeer, citizenB
		}, 403, consent.ReasonRequestURIOtherCitizen, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, returnURL)
			ref, clientID, citizen := tc.prepare(t, h, h.push(t, returnURL))

			status, body := h.resolve(t, ref, clientID, citizen)
			if status != tc.status {
				t.Errorf("status = %d, want %d (%v)", status, tc.status, body)
			}
			if tc.withBody && body["reason"] != string(tc.reason) {
				t.Errorf("reason = %v, want %s", body["reason"], tc.reason)
			}
		})
	}
}

// ── Decision ──────────────────────────────────────────────────────────────

func TestDecisionGrantReturnsACodeAndNoToken(t *testing.T) {
	h := newHarness(t, returnURL)
	uri := h.push(t, returnURL)
	h.resolve(t, uri, hypotheekPeer, citizenA)

	status, raw := h.decide(t, uri, citizenA, grantBoth)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, raw)
	}
	if bytes.Contains(raw, []byte("signed-consent-token")) || bytes.Contains(raw, []byte("c-1")) {
		t.Errorf("the decision carries the consent: %s", raw)
	}
	body := decode(t, raw)
	if body["outcome"] != "granted" || body["client_name"] != "Demo Hypotheekverlener BV" {
		t.Errorf("body = %v", body)
	}
	redirect, err := url.Parse(body["redirect_to"].(string))
	if err != nil {
		t.Fatal(err)
	}
	query := redirect.Query()
	if redirect.Scheme+"://"+redirect.Host+redirect.Path != returnURL || query.Get("code") == "" ||
		query.Get("state") != "af0ifjsldkj" || len(query) != 2 {
		t.Errorf("redirect_to = %s, want the return URL with code and state", redirect)
	}
	if h.register.consents() != 1 {
		t.Errorf("%d consents given, want 1", h.register.consents())
	}

	// The code is exchangeable for the token by the provider.
	token, err := h.portal.ExchangeCode(context.Background(), hypotheekPeer, consent.TokenRequest{
		Code: query.Get("code"), RedirectURI: returnURL, CodeVerifier: "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",
	})
	if err != nil || token.ConsentToken != "signed-consent-token" {
		t.Errorf("exchange = %+v, %v", token, err)
	}
}

func TestDecisionDenyReturnsAccessDenied(t *testing.T) {
	h := newHarness(t, returnURL)
	uri := h.push(t, returnURL)

	status, raw := h.decide(t, uri, citizenA, map[string]any{"decision": "deny"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, raw)
	}
	body := decode(t, raw)
	if body["outcome"] != "denied" || body["redirect_to"] != returnURL+"?error=access_denied&state=af0ifjsldkj" {
		t.Errorf("body = %v", body)
	}
	if h.register.consents() != 0 {
		t.Error("a refusal gave a consent")
	}
}

func TestDecisionFailureReturnsServerError(t *testing.T) {
	h := newHarness(t, returnURL)
	h.register.err = errors.New("register down")
	uri := h.push(t, returnURL)

	status, raw := h.decide(t, uri, citizenA, grantBoth)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, raw)
	}
	body := decode(t, raw)
	if body["outcome"] != "failed" || body["redirect_to"] != returnURL+"?error=server_error&state=af0ifjsldkj" {
		t.Errorf("body = %v", body)
	}
	if strings.Contains(string(raw), "register down") {
		t.Errorf("the failure is shown: %s", raw)
	}
	if status, _ := h.decide(t, uri, citizenA, grantBoth); status != http.StatusGone {
		t.Errorf("a failed decision left the reference usable: %d", status)
	}
}

func TestDecisionKeepsTheReturnURLQuery(t *testing.T) {
	registered := "https://hypotheek.example/return?flow=consent"
	h := newHarness(t, registered)
	uri := h.push(t, registered)

	_, raw := h.decide(t, uri, citizenA, map[string]any{"decision": "deny"})
	if got := decode(t, raw)["redirect_to"]; got != registered+"&error=access_denied&state=af0ifjsldkj" {
		t.Errorf("redirect_to = %v", got)
	}
}

func TestDecisionRefusesABadBodyAndKeepsTheReference(t *testing.T) {
	for name, body := range map[string]any{
		"no body":                  nil,
		"other decision":           map[string]any{"decision": "maybe"},
		"grant without data sets":  map[string]any{"decision": "grant"},
		"grant of empty data sets": map[string]any{"decision": "grant", "scopes": []string{}},
		"grant beyond the request": map[string]any{"decision": "grant", "scopes": []string{"bd:ib:2025", "lvg:vbo:eigendom"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, returnURL)
			uri := h.push(t, returnURL)

			if status, raw := h.decide(t, uri, citizenA, body); status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", status, raw)
			}
			if status, body := h.resolve(t, uri, hypotheekPeer, citizenA); status != http.StatusOK {
				t.Errorf("the reference is no longer usable: %d %v", status, body)
			}
			if h.register.consents() != 0 {
				t.Error("a bad decision gave a consent")
			}
		})
	}
}

func TestDecisionByAnotherCitizenIsForbidden(t *testing.T) {
	h := newHarness(t, returnURL)
	uri := h.push(t, returnURL)
	h.resolve(t, uri, hypotheekPeer, citizenA)

	status, raw := h.decide(t, uri, citizenB, grantBoth)
	if status != http.StatusForbidden || decode(t, raw)["reason"] != string(consent.ReasonRequestURIOtherCitizen) {
		t.Errorf("got %d %s, want 403 %s", status, raw, consent.ReasonRequestURIOtherCitizen)
	}
	if status, raw := h.decide(t, uri, citizenA, grantBoth); status != http.StatusOK {
		t.Errorf("the owner's decision afterwards: %d %s", status, raw)
	}
}

func TestDecisionNeedsALogin(t *testing.T) {
	h := newHarness(t, returnURL)
	if status, _ := h.decide(t, h.push(t, returnURL), "", grantBoth); status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", status)
	}
}
