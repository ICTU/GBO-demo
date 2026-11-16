package consentrequest

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type fakePortal struct {
	pushed    []PushedRequest
	exchanged []CodeExchange
	pushErr   error
	consent   Consent
	exchErr   error
}

func (p *fakePortal) Push(_ context.Context, req PushedRequest) (string, error) {
	p.pushed = append(p.pushed, req)
	if p.pushErr != nil {
		return "", p.pushErr
	}
	return "urn:ietf:params:oauth:request_uri:ref-1", nil
}

func (p *fakePortal) Exchange(_ context.Context, req CodeExchange) (Consent, error) {
	p.exchanged = append(p.exchanged, req)
	return p.consent, p.exchErr
}

type fakeSessions struct {
	mu       sync.Mutex
	sessions map[string]Session
}

func (f *fakeSessions) New(_ context.Context, s Session) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions == nil {
		f.sessions = map[string]Session{}
	}
	id := "session-" + string(rune('a'+len(f.sessions)))
	f.sessions[id] = s
	return id, nil
}

func (f *fakeSessions) Get(_ context.Context, id string) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return Session{}, ErrUnknownSession
	}
	return s, nil
}

func (f *fakeSessions) Update(_ context.Context, id string, change func(*Session) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return ErrUnknownSession
	}
	if err := change(&s); err != nil {
		return err
	}
	f.sessions[id] = s
	return nil
}

const redirectURI = "https://hypotheek.example/return"

func newRequester(portal *fakePortal) (*Requester, *fakeSessions) {
	sessions := &fakeSessions{}
	return &Requester{
		ClientID:    "99999999900000000300",
		RedirectURI: redirectURI,
		PortalURL:   "https://portal.example/",
		Portal:      portal,
		Sessions:    sessions,
	}, sessions
}

func start(t *testing.T, r *Requester, sessionID string) Started {
	t.Helper()
	started, err := r.Start(context.Background(), sessionID, Request{
		Service: "aangiftegegevens", Scopes: []string{"bd:ib:2025", "bd:ib:2024"}, ValidUntil: "2027-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return started
}

// The pushed request carries the configured client and return location and
// an S256 challenge of the verifier kept in the session; the browser is sent
// to the portal with only the client_id and the request_uri.
func TestStartPushesTheRequestAndKeepsTheVerifier(t *testing.T) {
	portal := &fakePortal{}
	r, sessions := newRequester(portal)

	started := start(t, r, "")

	pushed := portal.pushed[0]
	if pushed.ClientID != r.ClientID || pushed.RedirectURI != redirectURI || pushed.Service != "aangiftegegevens" ||
		strings.Join(pushed.Scopes, " ") != "bd:ib:2025 bd:ib:2024" || pushed.ValidUntil != "2027-01-01T00:00:00Z" {
		t.Errorf("pushed %+v", pushed)
	}
	s, _ := sessions.Get(context.Background(), started.SessionID)
	if s.Pending == nil || s.Pending.State != pushed.State || len(pushed.State) != 43 {
		t.Fatalf("pending %+v, pushed state %q", s.Pending, pushed.State)
	}
	if len(s.Pending.Verifier) != 43 || CodeChallenge(s.Pending.Verifier) != pushed.CodeChallenge {
		t.Errorf("verifier %q does not match challenge %q", s.Pending.Verifier, pushed.CodeChallenge)
	}
	u, err := url.Parse(started.RedirectTo)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != "https://portal.example/toestemming/start" {
		t.Fatalf("redirect %q", started.RedirectTo)
	}
	q := u.Query()
	if len(q) != 2 || q.Get("client_id") != r.ClientID || q.Get("request_uri") != "urn:ietf:params:oauth:request_uri:ref-1" {
		t.Errorf("redirect query %v, want only client_id and request_uri", q)
	}
}

// RFC 7636 appendix B.
func TestCodeChallengeIsS256(t *testing.T) {
	if got := CodeChallenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("challenge = %s", got)
	}
}

func TestEachStartHasItsOwnStateAndVerifier(t *testing.T) {
	portal := &fakePortal{}
	r, _ := newRequester(portal)
	start(t, r, "")
	start(t, r, "")
	if portal.pushed[0].State == portal.pushed[1].State || portal.pushed[0].CodeChallenge == portal.pushed[1].CodeChallenge {
		t.Error("two starts share a state or a challenge")
	}
}

func TestARefusedPushStartsNothing(t *testing.T) {
	refusal := &Refusal{Code: "invalid_scope", Reason: "UNKNOWN_SCOPE"}
	r, sessions := newRequester(&fakePortal{pushErr: refusal})
	_, err := r.Start(context.Background(), "", Request{Service: "s", Scopes: []string{"x"}})
	var got *Refusal
	if !errors.As(err, &got) || got.Reason != "UNKNOWN_SCOPE" {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if len(sessions.sessions) != 0 {
		t.Error("a refused push created a session")
	}
}

func TestADataSetWithWhiteSpaceIsNotPushed(t *testing.T) {
	portal := &fakePortal{}
	r, _ := newRequester(portal)
	_, err := r.Start(context.Background(), "", Request{Service: "s", Scopes: []string{"bd:ib:2025 bd:ib:2024"}})
	if !errors.Is(err, ErrInvalidRequest) || len(portal.pushed) != 0 {
		t.Fatalf("err = %v, pushed %d", err, len(portal.pushed))
	}
}

// A code is exchanged with the session's verifier; the session keeps the
// token, the result only names the consent.
func TestAGrantedReturnPutsTheTokenInTheSession(t *testing.T) {
	portal := &fakePortal{consent: Consent{Token: "a.b.c", ConsentID: "c-1", Scopes: []string{"bd:ib:2025"}}}
	r, sessions := newRequester(portal)
	started := start(t, r, "")
	pending, _ := sessions.Get(context.Background(), started.SessionID)

	done, err := r.Complete(context.Background(), started.SessionID, Return{Code: "code-1", State: portal.pushed[0].State})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Outcome != Granted || done.ConsentID != "c-1" || len(done.Scopes) != 1 {
		t.Errorf("completed %+v", done)
	}
	want := CodeExchange{ClientID: r.ClientID, Code: "code-1", RedirectURI: redirectURI, CodeVerifier: pending.Pending.Verifier}
	if portal.exchanged[0] != want {
		t.Errorf("exchanged %+v, want %+v", portal.exchanged[0], want)
	}
	if token, ok := r.ConsentToken(context.Background(), started.SessionID); !ok || token != "a.b.c" {
		t.Errorf("session token = %q, %v", token, ok)
	}
	if _, err := r.Complete(context.Background(), started.SessionID, Return{Code: "code-1", State: portal.pushed[0].State}); !errors.Is(err, ErrNoPendingRequest) {
		t.Errorf("second return: %v, want ErrNoPendingRequest", err)
	}
}

// A return with another state is refused and leaves the pending request, so
// the real return still works.
func TestAReturnWithAnotherStateIsRefused(t *testing.T) {
	portal := &fakePortal{consent: Consent{Token: "a.b.c", ConsentID: "c-1"}}
	r, _ := newRequester(portal)
	started := start(t, r, "")

	for _, ret := range []Return{{Code: "code-x", State: "other"}, {Error: "access_denied", State: "other"}} {
		if _, err := r.Complete(context.Background(), started.SessionID, ret); !errors.Is(err, ErrStateMismatch) {
			t.Fatalf("complete %+v: %v, want ErrStateMismatch", ret, err)
		}
	}
	if len(portal.exchanged) != 0 {
		t.Fatal("a mismatched state exchanged a code")
	}
	if _, err := r.Complete(context.Background(), started.SessionID, Return{Code: "code-1", State: portal.pushed[0].State}); err != nil {
		t.Fatalf("the real return: %v", err)
	}
}

func TestDeniedAndFailedReturnsAreOutcomes(t *testing.T) {
	for portalError, want := range map[string]Outcome{"access_denied": Denied, "server_error": Failed} {
		t.Run(portalError, func(t *testing.T) {
			portal := &fakePortal{}
			r, _ := newRequester(portal)
			started := start(t, r, "")
			done, err := r.Complete(context.Background(), started.SessionID, Return{Error: portalError, State: portal.pushed[0].State})
			if err != nil || done.Outcome != want || done.Error != portalError {
				t.Fatalf("completed %+v, %v; want %s", done, err, want)
			}
			if len(portal.exchanged) != 0 {
				t.Error("exchanged a code without one")
			}
			if _, ok := r.ConsentToken(context.Background(), started.SessionID); ok {
				t.Error("the session holds a token")
			}
		})
	}
}

func TestAFailedExchangeStoresNoToken(t *testing.T) {
	portal := &fakePortal{exchErr: &Refusal{Code: "invalid_grant"}}
	r, _ := newRequester(portal)
	started := start(t, r, "")
	_, err := r.Complete(context.Background(), started.SessionID, Return{Code: "code-1", State: portal.pushed[0].State})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != "invalid_grant" {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if _, ok := r.ConsentToken(context.Background(), started.SessionID); ok {
		t.Error("the session holds a token")
	}
}

func TestAReturnWithoutAPendingRequestIsRefused(t *testing.T) {
	r, _ := newRequester(&fakePortal{})
	for name, ret := range map[string]Return{
		"no code or error": {State: "s"},
		"code and error":   {Code: "c", Error: "access_denied", State: "s"},
		"no state":         {Code: "c"},
	} {
		if _, err := r.Complete(context.Background(), "", ret); !errors.Is(err, ErrInvalidReturn) {
			t.Errorf("%s: %v, want ErrInvalidReturn", name, err)
		}
	}
	if _, err := r.Complete(context.Background(), "unknown", Return{Code: "c", State: "s"}); !errors.Is(err, ErrNoPendingRequest) {
		t.Errorf("unknown session: %v, want ErrNoPendingRequest", err)
	}
}

// A new start in a session that holds a consent replaces the pending request
// and keeps the consent until a new one is granted.
func TestANewStartKeepsTheConsent(t *testing.T) {
	portal := &fakePortal{consent: Consent{Token: "a.b.c", ConsentID: "c-1"}}
	r, _ := newRequester(portal)
	first := start(t, r, "")
	if _, err := r.Complete(context.Background(), first.SessionID, Return{Code: "code-1", State: portal.pushed[0].State}); err != nil {
		t.Fatal(err)
	}
	second := start(t, r, first.SessionID)
	if second.SessionID != first.SessionID {
		t.Fatalf("session %q, want %q", second.SessionID, first.SessionID)
	}
	if token, ok := r.ConsentToken(context.Background(), first.SessionID); !ok || token != "a.b.c" {
		t.Errorf("token %q, %v", token, ok)
	}
	if third := start(t, r, "expired"); third.SessionID == "expired" {
		t.Error("an unknown session id was reused")
	}
}
