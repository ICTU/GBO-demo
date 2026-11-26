package fschttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/consent-portal-backend/consent"
)

// fakeRefresher is the consent register as far as a refresh needs it. The
// rules are the register's; this only answers what a test sets.
type fakeRefresher struct {
	consent.Store // nil: a refresh calls nothing else
	now           time.Time
	err           error
	calls         []refreshCall
}

type refreshCall struct {
	token  string
	client consent.PeerID
	scopes []string
}

func (f *fakeRefresher) Refresh(_ context.Context, token string, client consent.PeerID, scopes []string) (consent.Proof, error) {
	f.calls = append(f.calls, refreshCall{token: token, client: client, scopes: scopes})
	if f.err != nil {
		return consent.Proof{}, f.err
	}
	return consent.Proof{
		ConsentID:    "c-1",
		ConsentToken: "refreshed-consent-token",
		ExpiresAt:    f.now.Add(5*time.Minute + 500*time.Millisecond),
		Scopes:       []string{"bd:ib:2025"},
	}, nil
}

func refreshServer(t *testing.T, err error) (*fakeRefresher, func(peer string, form url.Values) (*http.Response, map[string]any)) {
	t.Helper()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	server, portal := newServer(t, fakeRegistrations{}, now)
	register := &fakeRefresher{now: now, err: err}
	portal.Consents = register
	return register, func(peer string, form url.Values) (*http.Response, map[string]any) {
		return exchange(t, server, peer, form)
	}
}

func refreshForm() url.Values {
	return url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"refresh-token-of-c-1"}}
}

func TestRefreshGivesANewConsentToken(t *testing.T) {
	register, refresh := refreshServer(t, nil)

	form := refreshForm()
	form.Set("client_id", hypotheekPeer)
	form.Set("scope", "bd:ib:2025")
	for range 2 { // not rotated: the same refresh token works again
		resp, body := refresh(hypotheekPeer, form)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body %v", resp.StatusCode, body)
		}
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
		if body["access_token"] != "refreshed-consent-token" || body["token_type"] != "Bearer" ||
			body["scope"] != "bd:ib:2025" || body["consent_id"] != "c-1" || body["expires_in"] != float64(301) {
			t.Errorf("body = %v", body)
		}
		if _, given := body["refresh_token"]; given {
			t.Errorf("a refresh gave a refresh token: %v", body)
		}
	}
	if len(register.calls) != 2 {
		t.Fatalf("register calls = %d, want 2", len(register.calls))
	}
	call := register.calls[0]
	if call.token != "refresh-token-of-c-1" || call.client != hypotheekPeer || strings.Join(call.scopes, " ") != "bd:ib:2025" {
		t.Errorf("the register was asked %+v", call)
	}
}

func TestRefreshRefusals(t *testing.T) {
	cases := []struct {
		name     string
		peer     string
		change   func(url.Values)
		register error
		status   int
		error    string
		asked    bool
	}{
		{"no Fsc-Authorization", "", func(url.Values) {}, nil, 401, "invalid_client", false},
		{"no refresh_token", hypotheekPeer, func(f url.Values) { f.Del("refresh_token") }, nil, 400, "invalid_request", false},
		{"repeated refresh_token", hypotheekPeer, func(f url.Values) { f.Add("refresh_token", "again") }, nil, 400, "invalid_request", false},
		{"client_id of another peer", hypotheekPeer, func(f url.Values) { f.Set("client_id", incassoPeer) }, nil, 400, "invalid_request", false},
		{"refused by the register", hypotheekPeer, func(url.Values) {}, fmt.Errorf("%w: revoked", consent.ErrInvalidGrant), 400, "invalid_grant", true},
		{"another client", incassoPeer, func(url.Values) {}, fmt.Errorf("%w: another client", consent.ErrInvalidGrant), 400, "invalid_grant", true},
		{"a narrower scope", hypotheekPeer, func(f url.Values) { f.Set("scope", "bd:ib:2024") }, fmt.Errorf("%w: narrower", consent.ErrInvalidScope), 400, "invalid_scope", true},
		{"register unreachable", hypotheekPeer, func(url.Values) {}, errors.New("connection refused"), 500, "server_error", true},
		{"oversized body", hypotheekPeer, func(f url.Values) { f.Set("padding", strings.Repeat("x", maxFormBytes)) }, nil, 413, "invalid_request", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			register, refresh := refreshServer(t, tc.register)
			form := refreshForm()
			tc.change(form)

			resp, body := refresh(tc.peer, form)
			if resp.StatusCode != tc.status || body["error"] != tc.error {
				t.Errorf("got %d %v, want %d %s", resp.StatusCode, body, tc.status, tc.error)
			}
			if _, leaked := body["access_token"]; leaked {
				t.Errorf("a refusal carries a token: %v", body)
			}
			if asked := len(register.calls) > 0; asked != tc.asked {
				t.Errorf("register asked = %v, want %v", asked, tc.asked)
			}
			if tc.status == 500 && body["error_description"] != nil {
				t.Errorf("a server error tells why: %v", body)
			}
		})
	}
}
