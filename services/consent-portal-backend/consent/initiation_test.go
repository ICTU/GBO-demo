package consent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

const (
	hypotheekPeer   PeerID = "99999999900000000300"
	incassoPeer     PeerID = "99999999900000000500"
	unregisteredURL        = "http://localhost:9001/elders"
)

// fakeRegistrations is the admission register: participants by peer ID, or
// a failure to read it.
type fakeRegistrations struct {
	participants map[PeerID]Registration
	err          error
	reads        int
}

func (f *fakeRegistrations) Registration(_ context.Context, peer PeerID) (Registration, error) {
	f.reads++
	if f.err != nil {
		return Registration{}, f.err
	}
	registration, ok := f.participants[peer]
	if !ok {
		return Registration{}, fmt.Errorf("%s: %w", peer, ErrUnknownParty)
	}
	return registration, nil
}

// demoRegistrations mirrors the demo configuration of the register: the
// active Hypotheekverlener with its registration and the inactive
// Incassobureau without one.
func demoRegistrations() *fakeRegistrations {
	return &fakeRegistrations{participants: map[PeerID]Registration{
		hypotheekPeer: {
			Name:            "Demo Hypotheekverlener BV",
			Active:          true,
			Services:        []RegisteredService{{Service: "aangiftegegevens", Purpose: "Hypotheek-aanvraag"}},
			ReturnLocations: []string{"http://localhost:9001/return"},
		},
		incassoPeer: {Name: "Demo Incassobureau BV", Active: false},
	}}
}

func checksPortal(registrations Registrations) *Portal {
	return &Portal{Registrations: registrations, KnownScopes: []string{"bd:ib:2025", "bd:ib:2024", "lvg:vbo:eigendom"}}
}

func validClaim() ClaimedRequest {
	return ClaimedRequest{
		ClientID:  string(hypotheekPeer),
		Service:   "aangiftegegevens",
		Scopes:    []string{"bd:ib:2025", "bd:ib:2024"},
		ReturnURL: "http://localhost:9001/return",
	}
}

// refusalCode returns the reason code of a refusal, and fails the test for
// any error that is not one.
func refusalCode(t *testing.T, err error) ReasonCode {
	t.Helper()
	if err == nil {
		t.Fatal("the request was accepted, want a refusal")
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error %v is not a *Refusal", err)
	}
	return refusal.Code
}

func TestVerifyRequestAcceptsARegisteredRequest(t *testing.T) {
	claim := validClaim()
	claim.Name = "Demo Hypotheekverlener BV"
	claim.Purpose = "Hypotheek-aanvraag"
	verified, err := checksPortal(demoRegistrations()).VerifyRequest(context.Background(), hypotheekPeer, claim)
	if err != nil {
		t.Fatalf("VerifyRequest: %v", err)
	}
	want := VerifiedRequest{
		Provider:  hypotheekPeer,
		Name:      "Demo Hypotheekverlener BV",
		Service:   "aangiftegegevens",
		Purpose:   "Hypotheek-aanvraag",
		Scopes:    []string{"bd:ib:2025", "bd:ib:2024"},
		ReturnURL: "http://localhost:9001/return",
	}
	if fmt.Sprint(verified) != fmt.Sprint(want) {
		t.Fatalf("verified = %+v, want %+v", verified, want)
	}
}

func TestVerifyRequestTakesNameAndPurposeFromTheRegistration(t *testing.T) {
	// Without a name or purpose in the request, the citizen still sees the
	// registered ones.
	verified, err := checksPortal(demoRegistrations()).VerifyRequest(context.Background(), hypotheekPeer, validClaim())
	if err != nil {
		t.Fatalf("VerifyRequest: %v", err)
	}
	if verified.Name != "Demo Hypotheekverlener BV" || verified.Purpose != "Hypotheek-aanvraag" {
		t.Fatalf("name %q, purpose %q: want the registered ones", verified.Name, verified.Purpose)
	}
}

func TestVerifyRequestRefusals(t *testing.T) {
	cases := []struct {
		name     string
		provider PeerID
		edit     func(*ClaimedRequest)
		want     ReasonCode
	}{
		{"no client_id", hypotheekPeer, func(c *ClaimedRequest) { c.ClientID = "" }, ReasonInitiationIncomplete},
		{"no service", hypotheekPeer, func(c *ClaimedRequest) { c.Service = "" }, ReasonInitiationIncomplete},
		{"no data sets", hypotheekPeer, func(c *ClaimedRequest) { c.Scopes = nil }, ReasonInitiationIncomplete},
		{"an empty data set", hypotheekPeer, func(c *ClaimedRequest) { c.Scopes = []string{"bd:ib:2025", ""} }, ReasonInitiationIncomplete},
		{"no return URL", hypotheekPeer, func(c *ClaimedRequest) { c.ReturnURL = "" }, ReasonInitiationIncomplete},
		{"unknown peer", "99999999900000009999", func(c *ClaimedRequest) { c.ClientID = "99999999900000009999" }, ReasonPartyNotActive},
		{"inactive peer", incassoPeer, func(c *ClaimedRequest) { c.ClientID = string(incassoPeer) }, ReasonPartyNotActive},
		{"client_id of another peer", hypotheekPeer, func(c *ClaimedRequest) { c.ClientID = "99999999900000001000" }, ReasonInitiationMismatch},
		{"service not registered", hypotheekPeer, func(c *ClaimedRequest) { c.Service = "installatieregister" }, ReasonInitiationMismatch},
		{"other name", hypotheekPeer, func(c *ClaimedRequest) { c.Name = "Betrouwbare Bank NV" }, ReasonInitiationMismatch},
		{"other purpose", hypotheekPeer, func(c *ClaimedRequest) { c.Purpose = "Marketing" }, ReasonInitiationMismatch},
		{"unknown data set", hypotheekPeer, func(c *ClaimedRequest) { c.Scopes = []string{"bd:ib:2025", "brp:alles"} }, ReasonUnknownScope},
		{"unregistered return URL", hypotheekPeer, func(c *ClaimedRequest) { c.ReturnURL = unregisteredURL }, ReasonReturnURLNotRegistered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claim := validClaim()
			tc.edit(&claim)
			_, err := checksPortal(demoRegistrations()).VerifyRequest(context.Background(), tc.provider, claim)
			if got := refusalCode(t, err); got != tc.want {
				t.Fatalf("code = %s, want %s (%v)", got, tc.want, err)
			}
		})
	}
}

func TestVerifyRequestMatchesTheReturnURLExactly(t *testing.T) {
	// No normalisation and no prefix match: context belongs in state.
	for _, returnURL := range []string{
		"http://localhost:9001/return/",
		"http://localhost:9001/return?next=/elders",
		"http://localhost:9001/return#x",
		"HTTP://localhost:9001/return",
		"http://localhost:9001/Return",
		"http://localhost:9001/return/../return",
		"http://localhost:9001",
	} {
		t.Run(returnURL, func(t *testing.T) {
			claim := validClaim()
			claim.ReturnURL = returnURL
			_, err := checksPortal(demoRegistrations()).VerifyRequest(context.Background(), hypotheekPeer, claim)
			if got := refusalCode(t, err); got != ReasonReturnURLNotRegistered {
				t.Fatalf("code = %s, want %s", got, ReasonReturnURLNotRegistered)
			}
		})
	}
}

func TestVerifyRequestChecksInOrder(t *testing.T) {
	// Every check fails at once; each fix reveals the next check. The
	// provider is inactive but registered, so that activating it is the
	// only fix the second step needs.
	registrations := demoRegistrations()
	registrations.participants[incassoPeer] = Registration{
		Name:            "Demo Incassobureau BV",
		Services:        []RegisteredService{{Service: "aangiftegegevens", Purpose: "Incasso"}},
		ReturnLocations: []string{"http://localhost:9001/return"},
	}
	portal := checksPortal(registrations)
	claim := ClaimedRequest{Scopes: []string{"brp:alles"}, ReturnURL: unregisteredURL, Name: "Betrouwbare Bank NV"}
	steps := []struct {
		want ReasonCode
		fix  func()
	}{
		{ReasonInitiationIncomplete, func() { claim.ClientID, claim.Service = string(hypotheekPeer), "aangiftegegevens" }},
		{ReasonPartyNotActive, func() {
			registration := registrations.participants[incassoPeer]
			registration.Active = true
			registrations.participants[incassoPeer] = registration
		}},
		{ReasonInitiationMismatch, func() { claim.ClientID = string(incassoPeer) }},
		{ReasonInitiationMismatch, func() { claim.Name = "" }},
		{ReasonUnknownScope, func() { claim.Scopes = []string{"bd:ib:2025"} }},
		{ReasonReturnURLNotRegistered, func() { claim.ReturnURL = "http://localhost:9001/return" }},
	}
	for i, step := range steps {
		_, err := portal.VerifyRequest(context.Background(), incassoPeer, claim)
		if got := refusalCode(t, err); got != step.want {
			t.Fatalf("step %d: code = %s, want %s (%v)", i, got, step.want, err)
		}
		step.fix()
	}
	if _, err := portal.VerifyRequest(context.Background(), incassoPeer, claim); err != nil {
		t.Fatalf("after every fix: %v", err)
	}
}

func TestVerifyRequestIncompleteNeedsNoRegister(t *testing.T) {
	registrations := demoRegistrations()
	claim := validClaim()
	claim.Service = ""
	_, err := checksPortal(registrations).VerifyRequest(context.Background(), hypotheekPeer, claim)
	if got := refusalCode(t, err); got != ReasonInitiationIncomplete {
		t.Fatalf("code = %s, want %s", got, ReasonInitiationIncomplete)
	}
	if registrations.reads != 0 {
		t.Fatalf("the register was read %d times for an incomplete request", registrations.reads)
	}
}

func TestVerifyRequestFailsClosedWithoutTheRegister(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")
	for name, portal := range map[string]*Portal{
		"unreachable":    checksPortal(&fakeRegistrations{err: cause}),
		"not configured": checksPortal(nil),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := portal.VerifyRequest(context.Background(), hypotheekPeer, validClaim())
			if got := refusalCode(t, err); got != ReasonAdmissionRegisterUnavailable {
				t.Fatalf("code = %s, want %s", got, ReasonAdmissionRegisterUnavailable)
			}
		})
	}
	_, err := checksPortal(&fakeRegistrations{err: cause}).VerifyRequest(context.Background(), hypotheekPeer, validClaim())
	if !errors.Is(err, cause) {
		t.Fatalf("the refusal %v does not carry its cause", err)
	}
}

func TestVerifyRequestDoesNotShareTheScopes(t *testing.T) {
	claim := validClaim()
	verified, err := checksPortal(demoRegistrations()).VerifyRequest(context.Background(), hypotheekPeer, claim)
	if err != nil {
		t.Fatalf("VerifyRequest: %v", err)
	}
	claim.Scopes[0] = "brp:alles"
	if slices.Contains(verified.Scopes, "brp:alles") {
		t.Fatal("changing the claimed scopes changed the verified ones")
	}
}
