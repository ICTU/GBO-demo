package authorization

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
)

// fakeClients and fakeIdentifier are in-memory stand-ins for the adapters.
type fakeClients map[string]*Client

func (f fakeClients) Client(id string) (*Client, bool) { c, ok := f[id]; return c, ok }

type fakeIdentifier map[string]string

func (f fakeIdentifier) Identify(hint string) (string, bool) { b, ok := f[hint]; return b, ok }

const (
	redirectURI = "https://qtsp.test/callback"
	resource    = "https://asip.test/authsrc-api"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := NewTokenIssuer("https://as.test", key)
	if err != nil {
		t.Fatal(err)
	}
	return NewService(Config{
		Issuer:     "https://as.test",
		Resource:   resource,
		Clients:    fakeClients{"qtsp": {ID: "qtsp", RedirectURIs: []string{redirectURI}, Scopes: map[string]bool{"verify": true}}},
		Identifier: fakeIdentifier{"frouke": "999991772"},
		Tokens:     tokens,
	})
}

func challenge(verifier string) string {
	d := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(d[:])
}

func validRequest() AuthorizationRequest {
	return AuthorizationRequest{
		ResponseType: "code", ClientID: "qtsp", RedirectURI: redirectURI, Scope: "verify", State: "s",
		CodeChallenge: challenge("a-code-verifier-of-at-least-forty-three-characters"), CodeChallengeMethod: "S256",
		LoginHint: "frouke",
	}
}

func TestAuthorize(t *testing.T) {
	s := newTestService(t)
	resp, err := s.Authorize(validRequest())
	if err != nil || resp.Err != nil || resp.Code == "" || resp.RedirectURI != redirectURI || resp.State != "s" {
		t.Fatalf("Authorize = %+v, %v; want a code for the registered redirect_uri", resp, err)
	}
	for _, tc := range []struct {
		name string
		edit func(*AuthorizationRequest)
		code string
	}{
		{"response_type", func(r *AuthorizationRequest) { r.ResponseType = "token" }, "unsupported_response_type"},
		{"scope outside the registration", func(r *AuthorizationRequest) { r.Scope = "retrieve" }, "invalid_scope"},
		{"no PKCE", func(r *AuthorizationRequest) { r.CodeChallenge = "" }, "invalid_request"},
		{"plain PKCE", func(r *AuthorizationRequest) { r.CodeChallengeMethod = "plain" }, "invalid_request"},
		{"another resource", func(r *AuthorizationRequest) { r.Resources = []string{"https://other.test"} }, "invalid_target"},
		{"unknown person", func(r *AuthorizationRequest) { r.LoginHint = "nobody" }, "access_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validRequest()
			tc.edit(&req)
			resp, err := s.Authorize(req)
			if err != nil || resp.Err == nil || resp.Err.Code != tc.code || resp.Code != "" {
				t.Fatalf("Authorize = %+v, %v; want error %s", resp, err, tc.code)
			}
		})
	}
	for name, edit := range map[string]func(*AuthorizationRequest){
		"unknown client":            func(r *AuthorizationRequest) { r.ClientID = "unknown" },
		"unregistered redirect_uri": func(r *AuthorizationRequest) { r.RedirectURI = "https://attacker.test" },
	} {
		t.Run(name, func(t *testing.T) {
			req := validRequest()
			edit(&req)
			var notRedirectable *NotRedirectableError
			if _, err := s.Authorize(req); !errors.As(err, &notRedirectable) {
				t.Fatalf("err = %v, want NotRedirectableError", err)
			}
		})
	}
}

func TestPKCEMatches(t *testing.T) {
	verifier := "a-code-verifier-of-at-least-forty-three-characters"
	if !pkceMatches(challenge(verifier), verifier) {
		t.Fatalf("matching verifier refused")
	}
	if pkceMatches(challenge(verifier), verifier+"x") || pkceMatches(challenge("short"), "short") {
		t.Fatalf("wrong or too short verifier accepted")
	}
}
