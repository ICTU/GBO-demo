package consent

import (
	"context"
	"testing"
)

// Consent must be informed: a grant the citizen did not confirm is refused
// with CONFIRMATION_MISSING before anything else happens (issue #598).

func TestGiveConsentWithoutConfirmationDoesNothing(t *testing.T) {
	p, bsnk, store := testPortal(t, nil)
	logbook := &fakeLogbook{}
	p.Logbook = logbook

	_, err := p.GiveConsent(context.Background(), citizenBSN, GiveInput{
		DienstverlenerOIN: "DV",
		Scopes:            []string{"bd:ib:2025"},
	})
	if got := refusalCode(t, err); got != ReasonConfirmationMissing {
		t.Fatalf("code = %s, want %s (%v)", got, ReasonConfirmationMissing, err)
	}
	if len(store.created) != 0 {
		t.Error("a consent was recorded without the citizen's confirmation")
	}
	activated, transformed := bsnk.calls()
	if activated != 0 || transformed != 0 || bsnk.listReads != 0 {
		t.Errorf("BSNk was called: %d activations, %d transformations, %d list reads", activated, transformed, bsnk.listReads)
	}
	if recorded := logbook.written(); len(recorded) != 0 {
		t.Errorf("logged %d processings for a refused grant", len(recorded))
	}
}

func TestDecideGrantWithoutConfirmationKeepsTheReference(t *testing.T) {
	p, _, store, bsnk := flowPortal(t)
	uri := resolved(t, p)

	unconfirmed := grant("bd:ib:2025")
	unconfirmed.InformationConfirmed = false
	_, err := p.Decide(context.Background(), citizenA, uri, unconfirmed)
	if got := refusalCode(t, err); got != ReasonConfirmationMissing {
		t.Fatalf("code = %s, want %s (%v)", got, ReasonConfirmationMissing, err)
	}
	if len(store.created) != 0 {
		t.Error("a consent was recorded without the citizen's confirmation")
	}
	if activated, transformed := bsnk.calls(); activated != 0 || transformed != 0 {
		t.Errorf("BSNk was called: %d activations, %d transformations", activated, transformed)
	}

	// The reference is not spent: once confirmed, the same grant goes through.
	decided, err := p.Decide(context.Background(), citizenA, uri, grant("bd:ib:2025"))
	if err != nil || decided.Outcome != OutcomeGranted {
		t.Fatalf("confirmed grant afterwards = %+v, %v", decided, err)
	}
}

func TestDecideDenyNeedsNoConfirmation(t *testing.T) {
	p, _, _, _ := flowPortal(t)
	uri := resolved(t, p)

	decided, err := p.Decide(context.Background(), citizenA, uri, Decision{})
	if err != nil || decided.Outcome != OutcomeDenied {
		t.Fatalf("deny without confirmation = %+v, %v", decided, err)
	}
}
