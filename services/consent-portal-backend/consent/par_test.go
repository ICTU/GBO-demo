package consent

import (
	"context"
	"strings"
	"testing"
	"time"
)

const testChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

func validPush() PushedRequest {
	return PushedRequest{
		ResponseType:        "code",
		ClientID:            string(hypotheekPeer),
		Service:             "aangiftegegevens",
		Scopes:              []string{"bd:ib:2025", "bd:ib:2024"},
		RedirectURI:         "http://localhost:9001/return",
		State:               "af0ifjsldkj",
		CodeChallenge:       testChallenge,
		CodeChallengeMethod: "S256",
	}
}

func parPortal(now time.Time) *Portal {
	p := checksPortal(demoRegistrations())
	p.Requests = NewRequestStore(10 * time.Minute)
	p.Now = func() time.Time { return now }
	return p
}

func TestPushRequestKeepsAVerifiedRequest(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	p := parPortal(now)
	push := validPush()
	push.ValidUntil = "2027-02-08T12:00:00Z"

	ref, err := p.PushRequest(context.Background(), hypotheekPeer, push)
	if err != nil {
		t.Fatalf("PushRequest: %v", err)
	}
	if !strings.HasPrefix(ref.RequestURI, "urn:ietf:params:oauth:request_uri:") {
		t.Errorf("request_uri = %q, want the RFC 9126 URN", ref.RequestURI)
	}
	if ref.ExpiresIn != 10*time.Minute {
		t.Errorf("expires in %v, want 10m", ref.ExpiresIn)
	}

	pending, found := p.Requests.Get(ref.RequestURI, now)
	if !found {
		t.Fatal("the request is not kept")
	}
	if pending.Request.Name != "Demo Hypotheekverlener BV" || pending.Request.Purpose != "Hypotheek-aanvraag" {
		t.Errorf("name and purpose = %q, %q, want those of the registration", pending.Request.Name, pending.Request.Purpose)
	}
	if pending.Request.Provider != hypotheekPeer || pending.State != push.State || pending.CodeChallenge != testChallenge {
		t.Errorf("pending request = %+v", pending)
	}
	if !pending.ValidUntil.Equal(time.Date(2027, 2, 8, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("valid until = %v", pending.ValidUntil)
	}
}

func TestPushRequestRefusesAnIncompleteOAuthRequest(t *testing.T) {
	for name, change := range map[string]func(*PushedRequest){
		"no response_type":       func(r *PushedRequest) { r.ResponseType = "" },
		"response_type token":    func(r *PushedRequest) { r.ResponseType = "token" },
		"no state":               func(r *PushedRequest) { r.State = "" },
		"no code_challenge":      func(r *PushedRequest) { r.CodeChallenge = "" },
		"no challenge method":    func(r *PushedRequest) { r.CodeChallengeMethod = "" },
		"plain challenge method": func(r *PushedRequest) { r.CodeChallengeMethod = "plain" },
		"not an S256 challenge":  func(r *PushedRequest) { r.CodeChallenge = "too-short" },
		"no client_id":           func(r *PushedRequest) { r.ClientID = "" },
		"no redirect_uri":        func(r *PushedRequest) { r.RedirectURI = "" },
		"valid_until not a time": func(r *PushedRequest) { r.ValidUntil = "in 90 days" },
	} {
		t.Run(name, func(t *testing.T) {
			registrations := demoRegistrations()
			p := checksPortal(registrations)
			p.Requests = NewRequestStore(time.Minute)
			push := validPush()
			change(&push)

			_, err := p.PushRequest(context.Background(), hypotheekPeer, push)
			if code := refusalCode(t, err); code != ReasonInitiationIncomplete {
				t.Errorf("code = %s, want %s", code, ReasonInitiationIncomplete)
			}
			if registrations.reads != 0 {
				t.Errorf("the register was read %d times for an incomplete request", registrations.reads)
			}
		})
	}
}

func TestPushRequestKeepsNothingItRefuses(t *testing.T) {
	now := time.Now()
	p := parPortal(now)
	push := validPush()
	push.ClientID = string(incassoPeer)

	_, err := p.PushRequest(context.Background(), hypotheekPeer, push)
	if code := refusalCode(t, err); code != ReasonInitiationMismatch {
		t.Errorf("code = %s, want %s", code, ReasonInitiationMismatch)
	}
	if n := len(p.Requests.requests); n != 0 {
		t.Errorf("%d requests kept after a refusal", n)
	}
}

func TestPushRequestNeedsARequestStore(t *testing.T) {
	p := checksPortal(demoRegistrations())
	if _, err := p.PushRequest(context.Background(), hypotheekPeer, validPush()); err == nil {
		t.Fatal("accepted without a request store")
	}
}

func TestRequestStoreExpiresRequests(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	store := NewRequestStore(10 * time.Minute)

	first, expiresAt, err := store.Put(PendingRequest{State: "first"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !expiresAt.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("expires at %v", expiresAt)
	}
	if _, found := store.Get(first, now.Add(10*time.Minute-time.Second)); !found {
		t.Error("the request expired before its lifetime ended")
	}
	if _, found := store.Get(first, now.Add(10*time.Minute)); found {
		t.Error("the request is still there after its lifetime")
	}
	if _, found := store.Get("urn:ietf:params:oauth:request_uri:unknown", now); found {
		t.Error("an unknown request URI was found")
	}

	second, _, err := store.Put(PendingRequest{State: "second"}, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Error("two requests got the same request URI")
	}
	if _, kept := store.requests[first]; kept {
		t.Error("the expired request was not dropped")
	}
}
