package issuance

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"gbo-demo/qtsp-reference/internal/login"
)

var frouke = login.Identity{
	FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24",
	Nationalities: []string{"NL", "BE"}, Means: login.MeansWallet,
}

func TestPendingAttestationIsIssuedOnceToMatchingPID(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if !store.Add(Request{
		Identity: frouke, Operation: "verify", Source: "RvIG", VerifiedAt: now,
		Evidence: []Evidence{{Name: "Familienaam", Outcome: "Match", Value: json.RawMessage(`{"family_name":"Jansen"}`)}}, ExpectedAttributes: 1,
	}) {
		t.Fatal("valid verification result was not accepted")
	}

	matching := frouke
	matching.Nationalities = []string{"BE", "NL"}
	document := store.Take(matching)
	if document == nil || document.AttestationType != AttestationType {
		t.Fatalf("document = %+v, want one %s", document, AttestationType)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(document.ID) {
		t.Errorf("document id = %q, want UUIDv4", document.ID)
	}
	if got := document.Attributes["subject_family_name"]; got != "Jansen" {
		t.Errorf("subject_family_name = %v", got)
	}
	if got := document.Attributes["qualification"]; got != Qualification {
		t.Errorf("qualification = %v", got)
	}
	lines, ok := document.Attributes["attribute_evidence"].(string)
	if !ok || !strings.Contains(lines, `Familienaam: Match`) || !strings.Contains(lines, `{"family_name":"Jansen"}`) {
		t.Errorf("attribute_evidence = %#v", document.Attributes["attribute_evidence"])
	}

	if second := store.Take(matching); second != nil {
		t.Fatalf("second document = %+v, want nil", second)
	}
}

func TestDifferentAndExpiredPIDReceiveNothingAndNewestRequestWins(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	request := Request{
		Identity: frouke, Operation: "retrieve", Source: "RvIG", VerifiedAt: now,
		Evidence: []Evidence{{Name: "Adres", Value: json.RawMessage(`{"city":"Rotterdam"}`)}}, ExpectedAttributes: 1,
	}
	store.Add(request)

	other := frouke
	other.FamilyName = "Bakker"
	if document := store.Take(other); document != nil {
		t.Fatalf("other citizen document = %+v, want nil", document)
	}

	// A second pending result for the same identity replaces the first.
	request.Source = "Newest source"
	store.Add(request)
	if document := store.Take(frouke); document == nil || document.Attributes["source_legal_name"] != "Newest source" {
		t.Fatalf("replacement document = %+v, want newest request", document)
	}

	expiredStore := NewStore(func() time.Time { return now })
	expiredStore.Add(request)
	now = now.Add(Lifetime + time.Second)
	if document := expiredStore.Take(frouke); document != nil {
		t.Fatalf("expired document = %+v, want nil", document)
	}
}

func TestMockLoginDoesNotSayWhichWalletMayReceiveTheStatement(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	mocked := frouke
	mocked.Means, mocked.FamilyName = login.MeansMock, "Testpersoon"
	store.Add(Request{
		Identity: mocked, Operation: "verify", Source: "RvIG", VerifiedAt: now,
		Evidence: []Evidence{{Name: "Familienaam", Outcome: "Match", Value: json.RawMessage(`"Testpersoon"`)}}, ExpectedAttributes: 1,
	})
	if document := store.Take(frouke); document != nil {
		t.Fatalf("wallet that differs from the mock login received %+v", document)
	}
}

func TestOnlySuccessfulResultsBecomePending(t *testing.T) {
	store := NewStore(time.Now)
	for _, tc := range []Request{
		{Identity: frouke, Operation: "verify", Source: "RvIG", Evidence: []Evidence{{Name: "Naam", Outcome: "NoMatch", Value: json.RawMessage(`"Jansen"`)}}},
		{Identity: frouke, Operation: "verify", Source: "RvIG", Evidence: []Evidence{{Name: "Naam", Outcome: "Unknown", Value: json.RawMessage(`"Jansen"`)}}},
		{Identity: frouke, Operation: "verify", Source: "RvIG", Evidence: []Evidence{{Name: "Naam", Outcome: "Match", Value: json.RawMessage(`"Jansen"`)}, {Name: "Adres", Outcome: "NoMatch", Value: json.RawMessage(`"Rotterdam"`)}}},
		{Identity: frouke, Operation: "verify", Source: "RvIG", Evidence: []Evidence{{Name: "Naam", Outcome: "Match"}}},
		{Identity: frouke, Operation: "retrieve", Source: "RvIG", Evidence: []Evidence{{Name: "Adres"}}},
	} {
		tc.ExpectedAttributes = len(tc.Evidence)
		if store.Add(tc) {
			t.Errorf("Add(%+v) = true, want false", tc)
		}
	}
	for _, tc := range []Request{
		{Identity: frouke, Operation: "verify", Source: "RvIG", Evidence: []Evidence{{Name: "Naam", Outcome: "Match", Value: json.RawMessage(`"Jansen"`)}}},
		{Identity: frouke, Operation: "verify", Source: "RvIG", Evidence: []Evidence{{Name: "Naam", Outcome: "MatchWithVariation", Value: json.RawMessage(`"jansen"`)}}},
		{Identity: frouke, Operation: "retrieve", Source: "RvIG", Evidence: []Evidence{{Name: "Adres", Value: json.RawMessage(`{"city":"Rotterdam"}`)}}},
	} {
		tc.ExpectedAttributes = len(tc.Evidence)
		if !store.Add(tc) {
			t.Errorf("Add(%+v) = false, want true", tc)
		}
	}
	if store.Add(Request{
		Identity: frouke, Operation: "verify", Source: "RvIG", ExpectedAttributes: 2,
		Evidence: []Evidence{{Name: "Naam", Outcome: "Match", Value: json.RawMessage(`"Jansen"`)}},
	}) {
		t.Error("incomplete evidence was accepted")
	}
}
