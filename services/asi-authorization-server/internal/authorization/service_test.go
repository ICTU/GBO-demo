package authorization

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

// fakeClients is an in-memory stand-in for the client registry.
type fakeClients map[string]*Client

func (f fakeClients) Client(id string) (*Client, bool) { c, ok := f[id]; return c, ok }

// fakeIdentifier identifies the user by a BSN of nine characters. It claims
// another means and PID data, which the core must not take over blindly:
// the core names the means itself.
type fakeIdentifier struct{}

func (fakeIdentifier) Identify(_ context.Context, credential string) (Identity, error) {
	switch {
	case credential == "no-bsn":
		return Identity{FamilyName: "Jansen"}, nil
	case len(credential) != 9:
		return Identity{}, errors.New("not nine characters")
	}
	return Identity{BSN: credential, Means: "forged", FamilyName: "Jansen"}, nil
}

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
		Issuer:      "https://as.test",
		Resources:   []Resource{{URL: resource, Name: "BRP"}, {URL: resource2, Name: "DUO"}},
		Clients:     fakeClients{"qtsp": {ID: "qtsp", Name: "Test QTSP", RedirectURIs: []string{redirectURI}, Scopes: map[string]bool{"verify": true}}},
		Identifiers: map[string]Identifier{"test-means": fakeIdentifier{}},
		Tokens:      tokens,
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
		Resources: []string{resource},
	}
}

func TestAuthorize(t *testing.T) {
	s := newTestService(t)
	resp, err := s.Authorize(validRequest())
	if err != nil || resp.Err != nil || resp.Code != "" || resp.Pending == nil || resp.RedirectURI != redirectURI || resp.State != "s" {
		t.Fatalf("Authorize = %+v, %v; want a pending transaction for the registered redirect_uri", resp, err)
	}
	if p := resp.Pending; p.ID == "" || p.Binding == "" || p.ClientName != "Test QTSP" || p.Source != "BRP" {
		t.Fatalf("transaction = %+v, want an id, a binding, the client's legal name and the BRP data service", p)
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

// The user's login or denial completes a transaction once, from the browser
// that started it, before it expires; the identification is the means's,
// behind the Identifier port.
func TestLogInAndDeny(t *testing.T) {
	ctx := context.Background()
	start := func(t *testing.T, s *Service) *Transaction {
		t.Helper()
		resp, err := s.Authorize(validRequest())
		if err != nil || resp.Pending == nil {
			t.Fatalf("Authorize = %+v, %v", resp, err)
		}
		return resp.Pending
	}
	login := func(tx *Transaction, credential string) Login {
		return Login{TransactionID: tx.ID, Binding: tx.Binding, Means: "test-means", Credential: credential}
	}
	t.Run("login approves, once, with the means named by the core", func(t *testing.T) {
		s := newTestService(t)
		tx := start(t, s)
		resp, err := s.LogIn(ctx, login(tx, "999991772"))
		if err != nil || resp.Code == "" || resp.Err != nil || resp.State != "s" {
			t.Fatalf("LogIn = %+v, %v; want a code", resp, err)
		}
		code, _ := s.codes.take(resp.Code)
		if code.identity.BSN != "999991772" || code.identity.Means != "test-means" {
			t.Fatalf("identity = %+v, want the BSN and the means test-means", code.identity)
		}
		if _, err := s.LogIn(ctx, login(tx, "999991772")); err == nil {
			t.Fatalf("a used transaction was accepted again")
		}
	})
	t.Run("not identified: the transaction stays", func(t *testing.T) {
		s := newTestService(t)
		tx := start(t, s)
		for _, credential := range []string{"123", "no-bsn"} {
			if _, err := s.LogIn(ctx, login(tx, credential)); !errors.Is(err, ErrNotIdentified) {
				t.Fatalf("LogIn with %q = %v, want ErrNotIdentified", credential, err)
			}
		}
		if resp, err := s.LogIn(ctx, login(tx, "999991772")); err != nil || resp.Code == "" {
			t.Fatalf("LogIn after a failed attempt = %+v, %v; want a code", resp, err)
		}
	})
	t.Run("deny", func(t *testing.T) {
		s := newTestService(t)
		tx := start(t, s)
		resp, err := s.Deny(tx.ID, tx.Binding)
		if err != nil || resp.Err == nil || resp.Err.Code != "access_denied" || resp.Code != "" {
			t.Fatalf("Deny = %+v, %v; want access_denied", resp, err)
		}
	})
	for name, run := range map[string]func(s *Service, tx *Transaction) error{
		"unknown means": func(s *Service, tx *Transaction) error {
			l := login(tx, "999991772")
			l.Means = "wallet"
			_, err := s.LogIn(ctx, l)
			return err
		},
		"unknown transaction": func(s *Service, tx *Transaction) error {
			l := login(tx, "999991772")
			l.TransactionID = "unknown"
			_, err := s.LogIn(ctx, l)
			return err
		},
		"another browser": func(s *Service, tx *Transaction) error {
			l := login(tx, "999991772")
			l.Binding = "other"
			_, err := s.LogIn(ctx, l)
			return err
		},
		"deny from another browser": func(s *Service, tx *Transaction) error {
			_, err := s.Deny(tx.ID, "other")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestService(t)
			var notRedirectable *NotRedirectableError
			if err := run(s, start(t, s)); !errors.As(err, &notRedirectable) {
				t.Fatalf("err = %v, want NotRedirectableError", err)
			}
		})
	}
	t.Run("another browser does not spoil the transaction", func(t *testing.T) {
		s := newTestService(t)
		tx := start(t, s)
		other := login(tx, "999991772")
		other.Binding = "other"
		_, _ = s.LogIn(ctx, other)
		_, _ = s.Deny(tx.ID, "other")
		if resp, err := s.LogIn(ctx, login(tx, "999991772")); err != nil || resp.Code == "" {
			t.Fatalf("LogIn after a wrong binding = %+v, %v; want a code", resp, err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		s := newTestService(t)
		tx := start(t, s)
		s.cfg.Now = func() time.Time { return time.Now().Add(TransactionLifetime + time.Second) }
		if _, err := s.LogIn(ctx, login(tx, "999991772")); err == nil {
			t.Fatalf("an expired transaction was accepted")
		}
	})
}
