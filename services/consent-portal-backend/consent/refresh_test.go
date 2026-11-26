package consent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// exchanged takes a pushed request through a granted decision and the code
// exchange by Hypotheek-BV, with tokens that expire five minutes after the
// grant, and returns what the exchange gave.
func exchanged(t *testing.T) (*Portal, *testClock, *memStore, Token) {
	t.Helper()
	p, clock, store, _ := flowPortal(t)
	store.tokenExpiresAt = clock.now.Add(5 * time.Minute)
	code := codeFor(t, p)
	clock.pass(10 * time.Second)
	token, err := p.ExchangeCode(context.Background(), hypotheekPeer, validExchange(code))
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	return p, clock, store, token
}

func TestADecisionBindsTheRefreshTokenToTheClient(t *testing.T) {
	p, _, store, _ := flowPortal(t)
	codeFor(t, p)
	if got := store.created[0].ClientPeer; got != hypotheekPeer {
		t.Errorf("client peer = %q, want the peer that pushed the request (%s)", got, hypotheekPeer)
	}
}

func TestExchangeCodeGivesExpiresInAndTheRefreshToken(t *testing.T) {
	_, _, _, token := exchanged(t)
	if token.RefreshToken != "refresh-token-of-c-1" {
		t.Errorf("refresh token = %q, want the one the register gave", token.RefreshToken)
	}
	// The token expires five minutes after the grant; the exchange came ten
	// seconds later.
	if want := 5*time.Minute - 10*time.Second; token.ExpiresIn != want {
		t.Errorf("expires in %v, want %v", token.ExpiresIn, want)
	}
}

func TestExchangeCodeGivesTheRefreshTokenOnlyToItsClient(t *testing.T) {
	p, _, _, _ := flowPortal(t)
	code := codeFor(t, p)
	token, err := p.ExchangeCode(context.Background(), incassoPeer, validExchange(code))
	if !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("exchange by another peer = %v, want ErrInvalidGrant", err)
	}
	if token.RefreshToken != "" || token.ConsentToken != "" {
		t.Errorf("a refused exchange gave %+v", token)
	}
}

func TestExpiresInIsNeverNegative(t *testing.T) {
	p, clock, store, _ := flowPortal(t)
	store.tokenExpiresAt = clock.now.Add(30 * time.Second)
	code := codeFor(t, p)
	clock.pass(45 * time.Second)
	token, err := p.ExchangeCode(context.Background(), hypotheekPeer, validExchange(code))
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if token.ExpiresIn != 0 {
		t.Errorf("expires in %v, want 0 for a token past its exp", token.ExpiresIn)
	}
}

func TestRefreshProofGivesANewTokenWithoutANewRefreshToken(t *testing.T) {
	p, clock, store, first := exchanged(t)
	clock.pass(4 * time.Minute)
	store.tokenExpiresAt = clock.now.Add(5 * time.Minute)

	for range 2 { // the token is not rotated: it works again
		token, err := p.RefreshProof(context.Background(), hypotheekPeer, RefreshRequest{RefreshToken: first.RefreshToken})
		if err != nil {
			t.Fatalf("RefreshProof: %v", err)
		}
		if token.ConsentToken != "refreshed-consent-token" || token.ConsentID != first.ConsentID ||
			strings.Join(token.Scopes, " ") != "bd:ib:2025" {
			t.Errorf("token = %+v", token)
		}
		if token.ExpiresIn != 5*time.Minute {
			t.Errorf("expires in %v, want 5m from the register's expires_at", token.ExpiresIn)
		}
		if token.RefreshToken != "" {
			t.Errorf("a refresh gave a new refresh token %q", token.RefreshToken)
		}
	}
	if len(store.refreshes) != 2 || store.refreshes[0].client != hypotheekPeer {
		t.Errorf("register calls = %+v, want two for %s", store.refreshes, hypotheekPeer)
	}
}

func TestRefreshProofPassesTheScopeOn(t *testing.T) {
	p, _, store, first := exchanged(t)
	if _, err := p.RefreshProof(context.Background(), hypotheekPeer, RefreshRequest{
		RefreshToken: first.RefreshToken,
		Scopes:       []string{"bd:ib:2025"},
	}); err != nil {
		t.Fatalf("RefreshProof: %v", err)
	}
	if got := strings.Join(store.refreshes[0].scopes, " "); got != "bd:ib:2025" {
		t.Errorf("scopes passed on = %q", got)
	}
}

func TestRefreshProofRefusals(t *testing.T) {
	cases := map[string]struct {
		client  PeerID
		token   func(Token) string
		prepare func(*testing.T, *Portal, *memStore, Token)
		want    error
	}{
		"another client": {incassoPeer, func(t Token) string { return t.RefreshToken }, nil, ErrInvalidGrant},
		"unknown token":  {hypotheekPeer, func(Token) string { return "unknown" }, nil, ErrInvalidGrant},
		"after a revocation": {hypotheekPeer, func(t Token) string { return t.RefreshToken },
			func(t *testing.T, p *Portal, _ *memStore, token Token) {
				if err := p.RevokeConsent(context.Background(), citizenA, token.ConsentID); err != nil {
					t.Fatalf("RevokeConsent: %v", err)
				}
			}, ErrInvalidGrant},
		"a scope the register refuses": {hypotheekPeer, func(t Token) string { return t.RefreshToken },
			func(_ *testing.T, _ *Portal, s *memStore, _ Token) {
				s.refreshErr = fmt.Errorf("%w: narrower", ErrInvalidScope)
			}, ErrInvalidScope},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p, _, store, first := exchanged(t)
			if tc.prepare != nil {
				tc.prepare(t, p, store, first)
			}
			token, err := p.RefreshProof(context.Background(), tc.client, RefreshRequest{RefreshToken: tc.token(first)})
			if !errors.Is(err, tc.want) {
				t.Fatalf("RefreshProof = %v, want %v", err, tc.want)
			}
			if token.ConsentToken != "" {
				t.Errorf("a refusal gave a token: %+v", token)
			}
		})
	}
}

func TestRefreshProofWithoutATokenAsksNobody(t *testing.T) {
	p, _, store, _ := exchanged(t)
	if _, err := p.RefreshProof(context.Background(), hypotheekPeer, RefreshRequest{}); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("RefreshProof = %v, want ErrInvalidGrant", err)
	}
	if len(store.refreshes) != 0 {
		t.Errorf("the register was asked: %+v", store.refreshes)
	}
}

func TestRefreshProofWhenTheRegisterFailsIsNoRefusal(t *testing.T) {
	p, _, store, first := exchanged(t)
	store.refreshErr = errors.New("connection refused")
	_, err := p.RefreshProof(context.Background(), hypotheekPeer, RefreshRequest{RefreshToken: first.RefreshToken})
	if err == nil || errors.Is(err, ErrInvalidGrant) || errors.Is(err, ErrInvalidScope) {
		t.Errorf("RefreshProof = %v, want the register's own failure", err)
	}
}

func TestTheDeveloperAPIGetsNoRefreshToken(t *testing.T) {
	p, _, store := testPortal(t, nil)
	granted, err := p.GiveConsent(context.Background(), BSN("111111111"), GiveInput{DienstverlenerOIN: "DV"})
	if err != nil {
		t.Fatalf("GiveConsent: %v", err)
	}
	if store.created[0].ClientPeer != "" || granted.RefreshToken != "" {
		t.Errorf("a consent without a client peer: draft %+v, granted %+v", store.created[0], granted)
	}
}
