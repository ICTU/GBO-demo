package authorization

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

// fakeClients and fakeIdentifier are in-memory stand-ins for the adapters.
type fakeClients map[string]*Client

func (f fakeClients) Client(id string) (*Client, bool) { c, ok := f[id]; return c, ok }

type fakeIdentifier map[string]Identity

func (f fakeIdentifier) Persons() []TestPerson {
	var out []TestPerson
	for id, p := range f {
		out = append(out, TestPerson{ID: id, Name: p.GivenName})
	}
	return out
}

func (f fakeIdentifier) Identify(id string) (Identity, bool) { p, ok := f[id]; return p, ok }

const (
	redirectURI = "https://qtsp.test/callback"
	resource    = "https://asip.test/brp/authsrc-api"
	resource2   = "https://asip.test/duo/authsrc-api"
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
		Resources:  []Resource{{URL: resource, Name: "BRP"}, {URL: resource2, Name: "DUO"}},
		Clients:    fakeClients{"qtsp": {ID: "qtsp", RedirectURIs: []string{redirectURI}, Scopes: map[string]bool{"verify": true}}},
		Identifier: fakeIdentifier{"frouke": {BSN: "999991772", GivenName: "Frouke"}},
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
		LoginHint: "frouke", Resources: []string{resource},
	}
}

func TestAuthorize(t *testing.T) {
	s := newTestService(t)
	resp, err := s.Authorize(validRequest())
	if err != nil || resp.Err != nil || resp.Code != "" || resp.Pending == nil || resp.RedirectURI != redirectURI || resp.State != "s" {
		t.Fatalf("Authorize = %+v, %v; want a pending transaction for the registered redirect_uri", resp, err)
	}
	if p := resp.Pending; p.ID == "" || p.Binding == "" || p.Preselected != "frouke" || len(p.Persons) != 1 || p.Source != "BRP" {
		t.Fatalf("transaction = %+v, want an id, a binding, the test persons, frouke preselected and the BRP data service", p)
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
		{"no resource", func(r *AuthorizationRequest) { r.Resources = nil }, "invalid_target"},
		{"two data services", func(r *AuthorizationRequest) { r.Resources = []string{resource, resource2} }, "invalid_target"},
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

// The user's decision completes a transaction once, from the browser that
// started it, before it expires.
func TestDecide(t *testing.T) {
	start := func(t *testing.T, s *Service) *Transaction {
		t.Helper()
		resp, err := s.Authorize(validRequest())
		if err != nil || resp.Pending == nil {
			t.Fatalf("Authorize = %+v, %v", resp, err)
		}
		return resp.Pending
	}
	t.Run("approve", func(t *testing.T) {
		s := newTestService(t)
		tx := start(t, s)
		resp, err := s.Decide(Decision{TransactionID: tx.ID, Binding: tx.Binding, PersonID: "frouke", Approve: true})
		if err != nil || resp.Code == "" || resp.Err != nil || resp.State != "s" {
			t.Fatalf("Decide = %+v, %v; want a code", resp, err)
		}
		if _, err := s.Decide(Decision{TransactionID: tx.ID, Binding: tx.Binding, PersonID: "frouke", Approve: true}); err == nil {
			t.Fatalf("a used transaction was accepted again")
		}
	})
	t.Run("deny", func(t *testing.T) {
		s := newTestService(t)
		tx := start(t, s)
		resp, err := s.Decide(Decision{TransactionID: tx.ID, Binding: tx.Binding})
		if err != nil || resp.Err == nil || resp.Err.Code != "access_denied" || resp.Code != "" {
			t.Fatalf("Decide = %+v, %v; want access_denied", resp, err)
		}
	})
	for name, d := range map[string]func(tx *Transaction) Decision{
		"unknown transaction": func(tx *Transaction) Decision {
			return Decision{TransactionID: "unknown", Binding: tx.Binding, PersonID: "frouke", Approve: true}
		},
		"another browser": func(tx *Transaction) Decision {
			return Decision{TransactionID: tx.ID, Binding: "other", PersonID: "frouke", Approve: true}
		},
		"unknown person": func(tx *Transaction) Decision {
			return Decision{TransactionID: tx.ID, Binding: tx.Binding, PersonID: "nobody", Approve: true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestService(t)
			var notRedirectable *NotRedirectableError
			if _, err := s.Decide(d(start(t, s))); !errors.As(err, &notRedirectable) {
				t.Fatalf("err = %v, want NotRedirectableError", err)
			}
		})
	}
	t.Run("another browser does not spoil the transaction", func(t *testing.T) {
		s := newTestService(t)
		tx := start(t, s)
		_, _ = s.Decide(Decision{TransactionID: tx.ID, Binding: "other", PersonID: "frouke", Approve: true})
		if resp, err := s.Decide(Decision{TransactionID: tx.ID, Binding: tx.Binding, PersonID: "frouke", Approve: true}); err != nil || resp.Code == "" {
			t.Fatalf("Decide after a wrong binding = %+v, %v; want a code", resp, err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		s := newTestService(t)
		tx := start(t, s)
		s.cfg.Now = func() time.Time { return time.Now().Add(TransactionLifetime + time.Second) }
		if _, err := s.Decide(Decision{TransactionID: tx.ID, Binding: tx.Binding, PersonID: "frouke", Approve: true}); err == nil {
			t.Fatalf("an expired transaction was accepted")
		}
	})
}
