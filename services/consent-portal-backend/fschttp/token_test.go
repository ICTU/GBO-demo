package fschttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/consent-portal-backend/consent"
)

// testVerifier is the code verifier of the challenge in validForm (RFC 7636
// appendix B).
const testVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

// tokenServer is a portal with a code store, and a code issued to
// Hypotheek-BV at issuedAt.
func tokenServer(t *testing.T, now, issuedAt time.Time) (*httptest.Server, string) {
	t.Helper()
	server, portal := newServer(t, fakeRegistrations{}, now)
	portal.Codes = consent.NewCodeStore(time.Minute)
	code, err := portal.Codes.Issue(consent.IssuedCode{
		Provider:      hypotheekPeer,
		RedirectURI:   returnURL,
		CodeChallenge: validForm().Get("code_challenge"),
		Consent:       consent.Granted{ConsentID: "c-1", ConsentToken: "signed-consent-token"},
		Scopes:        []string{"bd:ib:2025"},
	}, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	return server, code
}

func tokenForm(code string) url.Values {
	return url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {returnURL},
		"code_verifier": {testVerifier},
	}
}

func exchange(t *testing.T, server *httptest.Server, peer string, form url.Values) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+"/token", strings.NewReader(form.Encode()))
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

func TestTokenGivesTheConsentTokenOnce(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	server, code := tokenServer(t, now, now)

	form := tokenForm(code)
	form.Set("client_id", hypotheekPeer)
	resp, body := exchange(t, server, hypotheekPeer, form)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %v", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if body["access_token"] != "signed-consent-token" || body["token_type"] != "Bearer" ||
		body["scope"] != "bd:ib:2025" || body["consent_id"] != "c-1" {
		t.Errorf("body = %v", body)
	}

	resp, body = exchange(t, server, hypotheekPeer, tokenForm(code))
	if resp.StatusCode != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Errorf("second exchange: got %d %v, want 400 invalid_grant", resp.StatusCode, body)
	}
}

func TestTokenRefusesAnInvalidGrant(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		peer     string
		issuedAt time.Time
		change   func(url.Values)
	}{
		{"unknown code", hypotheekPeer, now, func(f url.Values) { f.Set("code", "unknown") }},
		{"after 60 seconds", hypotheekPeer, now.Add(-time.Minute), func(url.Values) {}},
		{"another peer", "99999999900000001000", now, func(url.Values) {}},
		{"other redirect_uri", hypotheekPeer, now, func(f url.Values) { f.Set("redirect_uri", returnURL+"/elders") }},
		{"wrong verifier", hypotheekPeer, now, func(f url.Values) { f.Set("code_verifier", strings.Repeat("a", 43)) }},
		{"no verifier", hypotheekPeer, now, func(f url.Values) { f.Del("code_verifier") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, code := tokenServer(t, now, tc.issuedAt)
			form := tokenForm(code)
			tc.change(form)

			resp, body := exchange(t, server, tc.peer, form)
			if resp.StatusCode != http.StatusBadRequest || body["error"] != "invalid_grant" {
				t.Errorf("got %d %v, want 400 invalid_grant", resp.StatusCode, body)
			}
			if _, leaked := body["access_token"]; leaked {
				t.Errorf("a refusal carries a token: %v", body)
			}
		})
	}
}

func TestTokenRefusesAnUnusableRequest(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		peer   string
		change func(url.Values)
		status int
		error  string
	}{
		{"no Fsc-Authorization", "", func(url.Values) {}, 401, "invalid_client"},
		{"other grant_type", hypotheekPeer, func(f url.Values) { f.Set("grant_type", "refresh_token") }, 400, "unsupported_grant_type"},
		{"no grant_type", hypotheekPeer, func(f url.Values) { f.Del("grant_type") }, 400, "invalid_request"},
		{"no code", hypotheekPeer, func(f url.Values) { f.Del("code") }, 400, "invalid_request"},
		{"no redirect_uri", hypotheekPeer, func(f url.Values) { f.Del("redirect_uri") }, 400, "invalid_request"},
		{"client_id of another peer", hypotheekPeer, func(f url.Values) { f.Set("client_id", incassoPeer) }, 400, "invalid_request"},
		{"repeated code", hypotheekPeer, func(f url.Values) { f.Add("code", "again") }, 400, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, code := tokenServer(t, now, now)
			form := tokenForm(code)
			tc.change(form)

			resp, body := exchange(t, server, tc.peer, form)
			if resp.StatusCode != tc.status || body["error"] != tc.error {
				t.Errorf("got %d %v, want %d %s", resp.StatusCode, body, tc.status, tc.error)
			}
		})
	}
}

func TestTokenWithoutACodeStoreIsAServerError(t *testing.T) {
	server, _ := newServer(t, fakeRegistrations{}, time.Now())
	resp, body := exchange(t, server, hypotheekPeer, tokenForm("any"))
	if resp.StatusCode != http.StatusInternalServerError || body["error"] != "server_error" {
		t.Errorf("got %d %v, want 500 server_error", resp.StatusCode, body)
	}
}
