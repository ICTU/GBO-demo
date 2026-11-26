package consent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// testVerifier is the code verifier of testChallenge (RFC 7636 appendix B).
const testVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

const (
	citizenA BSN = "999993653"
	citizenB BSN = "999990019"
)

// testClock is a settable clock for the portal under test.
type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time       { return c.now }
func (c *testClock) pass(d time.Duration) { c.now = c.now.Add(d) }

// flowPortal is a portal that can take a pushed request through resolve,
// decision and code exchange, with in-memory ports.
func flowPortal(t *testing.T) (*Portal, *testClock, *memStore, *fakeBSNk) {
	t.Helper()
	p, bsnk, store := testPortal(t, nil)
	clock := &testClock{now: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	p.Now = clock.Now
	p.Registrations = demoRegistrations()
	p.KnownScopes = []string{"bd:ib:2025", "bd:ib:2024", "lvg:vbo:eigendom"}
	p.Requests = NewRequestStore(10 * time.Minute)
	p.Codes = NewCodeStore(time.Minute)
	return p, clock, store, bsnk
}

// pushed pushes the valid test request, with valid_until 90 days ahead,
// and returns its request URI.
func pushed(t *testing.T, p *Portal) string {
	t.Helper()
	push := validPush()
	push.ValidUntil = p.now().Add(90 * 24 * time.Hour).Format(time.RFC3339)
	ref, err := p.PushRequest(context.Background(), hypotheekPeer, push)
	if err != nil {
		t.Fatalf("PushRequest: %v", err)
	}
	return ref.RequestURI
}

// resolved pushes the valid test request and resolves it for citizenA, as
// the consent screen does before a decision, and returns its request URI.
func resolved(t *testing.T, p *Portal) string {
	t.Helper()
	uri := pushed(t, p)
	resolve(t, p, uri)
	return uri
}

// resolve resolves uri for citizenA with the right client ID.
func resolve(t *testing.T, p *Portal, uri string) {
	t.Helper()
	if _, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer)); err != nil {
		t.Fatalf("ResolveRequest: %v", err)
	}
}

func grant(scopes ...string) Decision {
	return Decision{Grant: true, Scopes: scopes, InformationConfirmed: true}
}

func TestResolveRequestShowsTheRegistration(t *testing.T) {
	p, clock, _, _ := flowPortal(t)
	uri := pushed(t, p)

	request, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer))
	if err != nil {
		t.Fatalf("ResolveRequest: %v", err)
	}
	if request.ClientID != hypotheekPeer || request.ClientName != "Demo Hypotheekverlener BV" ||
		request.Service != "aangiftegegevens" || request.Purpose != "Hypotheek-aanvraag" {
		t.Errorf("request = %+v, want the registration of %s", request, hypotheekPeer)
	}
	if strings.Join(request.Scopes, " ") != "bd:ib:2025 bd:ib:2024" {
		t.Errorf("scopes = %v", request.Scopes)
	}
	if !request.ValidUntil.Equal(clock.now.Add(90 * 24 * time.Hour)) {
		t.Errorf("valid until = %v", request.ValidUntil)
	}

	// A reload by the same citizen resolves it again.
	if _, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer)); err != nil {
		t.Errorf("second resolve by the same citizen: %v", err)
	}
}

func TestResolveRequestRefusesAnUnusableReference(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, p *Portal, clock *testClock, uri string) (citizen BSN, ref, clientID string)
		want    ReasonCode
	}{
		{"unknown reference", func(_ *testing.T, _ *Portal, _ *testClock, _ string) (BSN, string, string) {
			return citizenA, "urn:ietf:params:oauth:request_uri:unknown", string(hypotheekPeer)
		}, ReasonRequestURIUnknown},
		{"expired reference", func(_ *testing.T, _ *Portal, clock *testClock, uri string) (BSN, string, string) {
			clock.pass(10 * time.Minute)
			return citizenA, uri, string(hypotheekPeer)
		}, ReasonRequestURIExpired},
		{"reference forgotten after its retention", func(_ *testing.T, _ *Portal, clock *testClock, uri string) (BSN, string, string) {
			clock.pass(20 * time.Minute)
			return citizenA, uri, string(hypotheekPeer)
		}, ReasonRequestURIUnknown},
		{"spent reference", func(t *testing.T, p *Portal, _ *testClock, uri string) (BSN, string, string) {
			resolve(t, p, uri)
			if _, err := p.Decide(context.Background(), citizenA, uri, Decision{}); err != nil {
				t.Fatalf("Decide: %v", err)
			}
			return citizenA, uri, string(hypotheekPeer)
		}, ReasonRequestURISpent},
		{"other client_id", func(_ *testing.T, _ *Portal, _ *testClock, uri string) (BSN, string, string) {
			return citizenA, uri, "99999999900000001000"
		}, ReasonClientIDMismatch},
		{"no client_id", func(_ *testing.T, _ *Portal, _ *testClock, uri string) (BSN, string, string) {
			return citizenA, uri, ""
		}, ReasonClientIDMismatch},
		{"other citizen", func(t *testing.T, p *Portal, _ *testClock, uri string) (BSN, string, string) {
			if _, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer)); err != nil {
				t.Fatalf("first resolve: %v", err)
			}
			return citizenB, uri, string(hypotheekPeer)
		}, ReasonRequestURIOtherCitizen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, clock, _, _ := flowPortal(t)
			uri := pushed(t, p)
			citizen, ref, clientID := tc.prepare(t, p, clock, uri)

			_, err := p.ResolveRequest(context.Background(), citizen, ref, clientID)
			if got := refusalCode(t, err); got != tc.want {
				t.Fatalf("code = %s, want %s (%v)", got, tc.want, err)
			}
		})
	}
}

func TestDecideRefusesAnInvalidDecisionAndKeepsTheReference(t *testing.T) {
	for name, decision := range map[string]Decision{
		"grant without data sets":      grant(),
		"grant with an empty set":      grant("bd:ib:2025", ""),
		"grant beyond the request":     grant("bd:ib:2025", "lvg:vbo:eigendom"),
		"grant of an unknown data set": grant("brp:alles"),
		"grant of a data set twice":    grant("bd:ib:2025", "bd:ib:2025"),
	} {
		t.Run(name, func(t *testing.T) {
			p, _, store, _ := flowPortal(t)
			uri := pushed(t, p)
			if _, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer)); err != nil {
				t.Fatalf("ResolveRequest: %v", err)
			}

			_, err := p.Decide(context.Background(), citizenA, uri, decision)
			if !errors.Is(err, ErrInvalidDecision) {
				t.Fatalf("Decide = %v, want ErrInvalidDecision", err)
			}
			if len(store.created) != 0 {
				t.Errorf("a consent was given for an invalid decision")
			}
			if _, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer)); err != nil {
				t.Errorf("the reference is no longer usable: %v", err)
			}
			if decided, err := p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025")); err != nil || decided.Outcome != OutcomeGranted {
				t.Errorf("a valid decision afterwards = %+v, %v", decided, err)
			}
		})
	}
}

func TestDecideGrantGivesConsentAndACode(t *testing.T) {
	p, clock, store, _ := flowPortal(t)
	uri := pushed(t, p)
	if _, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer)); err != nil {
		t.Fatalf("ResolveRequest: %v", err)
	}
	clock.pass(time.Minute)

	decided, err := p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025"))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decided.Outcome != OutcomeGranted || decided.Code == "" || decided.Failure != nil {
		t.Fatalf("decided = %+v, want a grant with a code", decided)
	}
	if decided.ReturnURL != "http://localhost:9001/return" || decided.State != "af0ifjsldkj" || decided.ClientName != "Demo Hypotheekverlener BV" {
		t.Errorf("decided = %+v", decided)
	}
	if text := fmt.Sprintf("%+v", decided); strings.Contains(text, "signed-consent-token") {
		t.Errorf("the decision carries the consent token: %s", text)
	}

	if len(store.created) != 1 {
		t.Fatalf("%d consents given, want 1", len(store.created))
	}
	draft := store.created[0]
	if draft.DienstverlenerOIN != string(hypotheekPeer) || strings.Join(draft.Scopes, " ") != "bd:ib:2025" || draft.UseCase != "Hypotheek-aanvraag" {
		t.Errorf("consent = %+v", draft)
	}
	if want := int((90*24*time.Hour - time.Minute).Seconds()); draft.ValiditySeconds != want {
		t.Errorf("validity = %d s, want %d s (up to valid_until)", draft.ValiditySeconds, want)
	}
	if draft.SubjectRef != subjectRefOf(t, citizenA) {
		t.Errorf("subject = %q, want the citizen's reference", draft.SubjectRef)
	}

	_, err = p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025"))
	if code := refusalCode(t, err); code != ReasonRequestURISpent {
		t.Errorf("second decision: code = %s, want %s", code, ReasonRequestURISpent)
	}
}

func TestDecideWithoutValidUntilUsesTheDefaultValidity(t *testing.T) {
	p, _, store, _ := flowPortal(t)
	ref, err := p.PushRequest(context.Background(), hypotheekPeer, validPush())
	if err != nil {
		t.Fatalf("PushRequest: %v", err)
	}
	resolve(t, p, ref.RequestURI)
	if _, err := p.Decide(context.Background(), citizenA, ref.RequestURI, grant("bd:ib:2024")); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got, want := store.created[0].ValiditySeconds, int(DefaultConsentValidity.Seconds()); got != want {
		t.Errorf("validity = %d s, want %d s", got, want)
	}
}

func TestDecideDenyGivesNoConsent(t *testing.T) {
	p, _, store, bsnk := flowPortal(t)
	uri := resolved(t, p)

	decided, err := p.Decide(context.Background(), citizenA, uri, Decision{Grant: false, Scopes: []string{"brp:alles"}})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decided.Outcome != OutcomeDenied || decided.Code != "" || decided.State != "af0ifjsldkj" {
		t.Errorf("decided = %+v, want a refusal with the state", decided)
	}
	if activated, transformed := bsnk.calls(); len(store.created) != 0 || activated != 0 || transformed != 0 {
		t.Errorf("a refusal reached BSNk or the register")
	}
	_, err = p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer))
	if code := refusalCode(t, err); code != ReasonRequestURISpent {
		t.Errorf("code after the refusal = %s, want %s", code, ReasonRequestURISpent)
	}
}

func TestDecideReportsAFailedGrant(t *testing.T) {
	p, _, store, bsnk := flowPortal(t)
	bsnk.err = errors.New("bsnk down")
	uri := resolved(t, p)

	decided, err := p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025"))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if decided.Outcome != OutcomeFailed || decided.Failure == nil || decided.Code != "" || decided.State != "af0ifjsldkj" {
		t.Errorf("decided = %+v, want a failure with the state and no code", decided)
	}
	if len(store.created) != 0 {
		t.Error("a consent was given")
	}
	_, err = p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025"))
	if code := refusalCode(t, err); code != ReasonRequestURISpent {
		t.Errorf("code after the failure = %s, want %s", code, ReasonRequestURISpent)
	}
}

func TestDecideRefusesAnExpiredReference(t *testing.T) {
	p, clock, store, _ := flowPortal(t)
	uri := pushed(t, p)
	if _, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer)); err != nil {
		t.Fatalf("ResolveRequest: %v", err)
	}
	clock.pass(10 * time.Minute)

	_, err := p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025"))
	if code := refusalCode(t, err); code != ReasonRequestURIExpired {
		t.Errorf("code = %s, want %s", code, ReasonRequestURIExpired)
	}
	if len(store.created) != 0 {
		t.Error("a decision on an expired reference gave a consent")
	}
}

func TestDecideWithoutACodeStoreSpendsNothing(t *testing.T) {
	p, _, store, _ := flowPortal(t)
	uri := pushed(t, p)
	resolve(t, p, uri)
	p.Codes = nil

	if _, err := p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025")); err == nil {
		t.Fatal("decided without a code store")
	}
	if len(store.created) != 0 {
		t.Error("a consent was given without a code store")
	}
	if _, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer)); err != nil {
		t.Errorf("the reference is no longer usable: %v", err)
	}
}

func TestDecideByAnotherCitizenLeavesTheReference(t *testing.T) {
	p, _, store, _ := flowPortal(t)
	uri := pushed(t, p)
	if _, err := p.ResolveRequest(context.Background(), citizenA, uri, string(hypotheekPeer)); err != nil {
		t.Fatalf("ResolveRequest: %v", err)
	}

	_, err := p.Decide(context.Background(), citizenB, uri, grant("bd:ib:2025"))
	if code := refusalCode(t, err); code != ReasonRequestURIOtherCitizen {
		t.Fatalf("code = %s, want %s", code, ReasonRequestURIOtherCitizen)
	}
	if len(store.created) != 0 {
		t.Error("another citizen's decision gave a consent")
	}
	if decided, err := p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025")); err != nil || decided.Outcome != OutcomeGranted {
		t.Errorf("the owner's decision afterwards = %+v, %v", decided, err)
	}
}

func TestOnlyOneDecisionSpendsAReference(t *testing.T) {
	p, _, store, _ := flowPortal(t)
	uri := resolved(t, p)

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted, spent := 0, 0
	for range 8 {
		wg.Go(func() {
			decided, err := p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025"))
			mu.Lock()
			defer mu.Unlock()
			var refusal *Refusal
			switch {
			case err == nil && decided.Outcome == OutcomeGranted:
				granted++
			case errors.As(err, &refusal) && refusal.Code == ReasonRequestURISpent:
				spent++
			default:
				t.Errorf("decision = %+v, %v", decided, err)
			}
		})
	}
	wg.Wait()
	if granted != 1 || spent != 7 || len(store.created) != 1 {
		t.Errorf("granted %d, spent %d, consents %d; want 1, 7, 1", granted, spent, len(store.created))
	}
}

// TestDecideRefusesAReferenceThatWasNotResolved: a decision is accepted
// only on a reference the citizen resolved, which checked the client ID.
// Without that resolve it is refused, gives no consent and leaves the
// reference usable.
func TestDecideRefusesAReferenceThatWasNotResolved(t *testing.T) {
	for name, decision := range map[string]Decision{"grant": grant("bd:ib:2025"), "deny": {}} {
		t.Run(name, func(t *testing.T) {
			p, _, store, bsnk := flowPortal(t)
			uri := pushed(t, p)

			_, err := p.Decide(context.Background(), citizenA, uri, decision)
			if code := refusalCode(t, err); code != ReasonRequestURINotResolved {
				t.Fatalf("code = %s, want %s", code, ReasonRequestURINotResolved)
			}
			if activated, transformed := bsnk.calls(); len(store.created) != 0 || activated != 0 || transformed != 0 {
				t.Error("a decision without a resolve reached BSNk or the register")
			}
			resolve(t, p, uri)
			if decided, err := p.Decide(context.Background(), citizenA, uri, decision); err != nil {
				t.Errorf("the decision after the resolve = %+v, %v", decided, err)
			}
		})
	}
}

// codeFor takes a pushed request through a granted decision and returns
// the code.
func codeFor(t *testing.T, p *Portal) string {
	t.Helper()
	decided, err := p.Decide(context.Background(), citizenA, resolved(t, p), grant("bd:ib:2025"))
	if err != nil || decided.Outcome != OutcomeGranted {
		t.Fatalf("Decide = %+v, %v", decided, err)
	}
	return decided.Code
}

func validExchange(code string) TokenRequest {
	return TokenRequest{Code: code, RedirectURI: "http://localhost:9001/return", CodeVerifier: testVerifier}
}

func TestExchangeCodeGivesTheConsentTokenOnce(t *testing.T) {
	p, _, _, _ := flowPortal(t)
	code := codeFor(t, p)

	token, err := p.ExchangeCode(context.Background(), hypotheekPeer, validExchange(code))
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if token.ConsentToken != "signed-consent-token" || token.ConsentID == "" || strings.Join(token.Scopes, " ") != "bd:ib:2025" {
		t.Errorf("token = %+v", token)
	}

	if _, err := p.ExchangeCode(context.Background(), hypotheekPeer, validExchange(code)); !errors.Is(err, ErrInvalidGrant) {
		t.Errorf("second exchange = %v, want ErrInvalidGrant", err)
	}
}

func TestExchangeCodeRefusesAnInvalidGrant(t *testing.T) {
	cases := map[string]struct {
		provider PeerID
		edit     func(*TokenRequest)
		wait     time.Duration
	}{
		"unknown code":          {hypotheekPeer, func(r *TokenRequest) { r.Code = "unknown" }, 0},
		"after 60 seconds":      {hypotheekPeer, func(*TokenRequest) {}, time.Minute},
		"another provider":      {"99999999900000001000", func(*TokenRequest) {}, 0},
		"other redirect_uri":    {hypotheekPeer, func(r *TokenRequest) { r.RedirectURI = unregisteredURL }, 0},
		"wrong verifier":        {hypotheekPeer, func(r *TokenRequest) { r.CodeVerifier = strings.Repeat("a", 43) }, 0},
		"no verifier":           {hypotheekPeer, func(r *TokenRequest) { r.CodeVerifier = "" }, 0},
		"challenge as verifier": {hypotheekPeer, func(r *TokenRequest) { r.CodeVerifier = testChallenge }, 0},
		"verifier too short":    {hypotheekPeer, func(r *TokenRequest) { r.CodeVerifier = testVerifier[:42] }, 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p, clock, _, _ := flowPortal(t)
			code := codeFor(t, p)
			clock.pass(tc.wait)
			request := validExchange(code)
			tc.edit(&request)

			if _, err := p.ExchangeCode(context.Background(), tc.provider, request); !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("ExchangeCode = %v, want ErrInvalidGrant", err)
			}
			if tc.wait > 0 || name == "unknown code" {
				return
			}
			// A failed attempt does not burn the code for its provider.
			if _, err := p.ExchangeCode(context.Background(), hypotheekPeer, validExchange(code)); err != nil {
				t.Errorf("the right exchange afterwards: %v", err)
			}
		})
	}
}

func TestOnlyOneExchangeRedeemsACode(t *testing.T) {
	p, _, _, _ := flowPortal(t)
	code := codeFor(t, p)

	var wg sync.WaitGroup
	var mu sync.Mutex
	exchanged, refused := 0, 0
	for range 8 {
		wg.Go(func() {
			token, err := p.ExchangeCode(context.Background(), hypotheekPeer, validExchange(code))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && token.ConsentToken == "signed-consent-token":
				exchanged++
			case errors.Is(err, ErrInvalidGrant):
				refused++
			default:
				t.Errorf("exchange = %+v, %v", token, err)
			}
		})
	}
	wg.Wait()
	if exchanged != 1 || refused != 7 {
		t.Errorf("exchanged %d, refused %d; want 1, 7", exchanged, refused)
	}
}

func TestCodeStoreDropsExpiredCodes(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	store := NewCodeStore(time.Minute)
	first, err := store.Issue(IssuedCode{}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Issue(IssuedCode{}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("two codes are equal")
	}
	if _, kept := store.codes[first]; kept {
		t.Error("the expired code was not dropped")
	}
}
