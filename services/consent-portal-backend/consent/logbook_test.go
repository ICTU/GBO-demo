package consent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeLogbook records what the core filed, and can refuse, so the fail-closed
// rule is testable at the core boundary rather than only through HTTP.
type fakeLogbook struct {
	mu       sync.Mutex
	recorded []Processing
	err      error
}

func (f *fakeLogbook) Record(_ context.Context, p Processing) error {
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, p)
	return nil
}

func (f *fakeLogbook) written() []Processing {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Processing(nil), f.recorded...)
}

func portalWithLogbook(t *testing.T) (*Portal, *memStore, *fakeLogbook) {
	t.Helper()
	portal, _, store := testPortal(t, nil)
	logbook := &fakeLogbook{}
	portal.Logbook = logbook
	return portal, store, logbook
}

// A BSN that is not a substring of the dienstverlener OIN used below: the
// leak assertion is a plain substring check, stricter than the logbook's own
// elfproef guard, and a coincidental overlap would make it fire on nothing.
const citizenBSN = BSN("999991772")

// Turning a BSN into pseudonyms is the processing that makes every processing
// after it BSN-free. It is a Dataverwerking in its own right and has to be in
// the logboek.
func TestGiveConsentLogsThePseudonymisation(t *testing.T) {
	portal, _, logbook := portalWithLogbook(t)

	if _, err := portal.GiveConsent(context.Background(), citizenBSN, GiveInput{
		DienstverlenerOIN: "00000001234567890000",
		Scopes:            []string{"bd:ib:2025"},
	}); err != nil {
		t.Fatalf("give consent: %v", err)
	}

	recorded := logbook.written()
	if len(recorded) != 1 {
		t.Fatalf("recorded %d processings, want 1: %+v", len(recorded), recorded)
	}
	processing := recorded[0]
	if processing.Activity != pseudonymisationActivity {
		t.Errorf("activity = %q, want %q", processing.Activity, pseudonymisationActivity)
	}
	// The record names the Betrokkene by the portal-scoped reference it just
	// derived — not the BSN it started from, and not the PI it produced for
	// the dienstverlener.
	if want := fakeSubjectRef(citizenBSN, testPortalOIN); processing.Subject != want {
		t.Errorf("subject = %q, want the portal-scoped reference %q", processing.Subject, want)
	}
	if processing.Failed {
		t.Error("a successful pseudonymisation must not be recorded as failed")
	}
	if processing.End.Before(processing.Start) {
		t.Error("end precedes start")
	}
	if got := processing.Attributes["gbo.pseudonimisering.aanleiding"]; got != "toestemming-verlenen" {
		t.Errorf("aanleiding = %v", got)
	}
}

// Fail-closed, and early enough to matter: the record is filed before the
// consent is created, so a refused record leaves no consent behind.
func TestGiveConsentThatCannotBeLoggedCreatesNoConsent(t *testing.T) {
	portal, store, logbook := portalWithLogbook(t)
	logbook.err = errors.New("logboek unreachable")

	_, err := portal.GiveConsent(context.Background(), citizenBSN, GiveInput{
		DienstverlenerOIN: "00000001234567890000",
		Scopes:            []string{"bd:ib:2025"},
	})
	if err == nil {
		t.Fatal("an unlogged consent must not be granted")
	}
	if !strings.Contains(err.Error(), "log pseudonymisation") {
		t.Errorf("error should name the logging failure, got %v", err)
	}
	store.mu.Lock()
	created := len(store.created)
	store.mu.Unlock()
	if created != 0 {
		t.Fatalf("the consent register was written to %d times despite the refused record", created)
	}
}

// Listing and revoking both start by deriving the subject reference, which is
// the same processing. Logging it in one place is what keeps either flow from
// quietly going unlogged.
func TestListAndRevokeLogTheirPseudonymisation(t *testing.T) {
	for name, exercise := range map[string]struct {
		run        func(*testing.T, *Portal, *memStore) error
		aanleiding string
	}{
		"listing": {
			run: func(_ *testing.T, portal *Portal, _ *memStore) error {
				_, err := portal.ListConsents(context.Background(), citizenBSN)
				return err
			},
			aanleiding: "toestemmingen-inzien",
		},
		"revoking": {
			run: func(t *testing.T, portal *Portal, _ *memStore) error {
				granted, err := portal.GiveConsent(context.Background(), citizenBSN, GiveInput{
					DienstverlenerOIN: "00000001234567890000",
					Scopes:            []string{"bd:ib:2025"},
				})
				if err != nil {
					t.Fatalf("seed a consent: %v", err)
				}
				return portal.RevokeConsent(context.Background(), citizenBSN, granted.ConsentID)
			},
			aanleiding: "toestemming-intrekken",
		},
	} {
		t.Run(name, func(t *testing.T) {
			portal, store, logbook := portalWithLogbook(t)
			if err := exercise.run(t, portal, store); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			var found bool
			for _, processing := range logbook.written() {
				if processing.Attributes["gbo.pseudonimisering.aanleiding"] == exercise.aanleiding {
					found = true
				}
			}
			if !found {
				t.Fatalf("no record with aanleiding %q: %+v", exercise.aanleiding, logbook.written())
			}
		})
	}
}

// No BSN may reach a record, whatever the flow. The fake PI and subject
// reference are hashes rather than "PI-"+bsn, so this assertion is not
// vacuous.
func TestNoBSNInAnyRecordedProcessing(t *testing.T) {
	portal, _, logbook := portalWithLogbook(t)

	granted, err := portal.GiveConsent(context.Background(), citizenBSN, GiveInput{
		DienstverlenerOIN: "00000001234567890000",
		Scopes:            []string{"bd:ib:2025"},
	})
	if err != nil {
		t.Fatalf("give consent: %v", err)
	}
	if _, err := portal.ListConsents(context.Background(), citizenBSN); err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := portal.RevokeConsent(context.Background(), citizenBSN, granted.ConsentID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	recorded := logbook.written()
	if len(recorded) != 3 {
		t.Fatalf("recorded %d processings, want one per flow: %+v", len(recorded), recorded)
	}
	for _, processing := range recorded {
		if strings.Contains(string(processing.Subject), string(citizenBSN)) {
			t.Fatalf("record subject contains the BSN: %q", processing.Subject)
		}
		if processing.Subject == "" {
			t.Fatal("every record must name a Betrokkene")
		}
		for key, value := range processing.Attributes {
			if text, isText := value.(string); isText && strings.Contains(text, string(citizenBSN)) {
				t.Fatalf("attribute %q contains the BSN", key)
			}
		}
	}
}

// A deployment that is not part of an LDV chain writes no records and keeps
// working. The nil check lives in the core so no caller has to remember it.
func TestAPortalWithoutALogbookStillWorks(t *testing.T) {
	portal, _, _ := testPortal(t, nil)

	if _, err := portal.GiveConsent(context.Background(), citizenBSN, GiveInput{
		DienstverlenerOIN: "00000001234567890000",
		Scopes:            []string{"bd:ib:2025"},
	}); err != nil {
		t.Fatalf("give consent without a logbook: %v", err)
	}
}
