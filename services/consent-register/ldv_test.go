package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"
)

// The portal-scoped reference this register works with. It is a pseudonym,
// derived by the portal; the register never sees the BSN behind it.
const testSubjectRef = "EP-3f9a1c77b2"

// registerUnderTest returns the register's URL plus the pieces a test needs to
// drain the outbox: records are committed with the mutation and delivered
// afterwards, so a test asserts on both halves rather than on a single write.
func registerUnderTest(t *testing.T, logbook *ldvtest.Logbook) (string, ConsentStore, *registerLogbook) {
	t.Helper()
	issuer, err := NewConsentIssuer(config{SigningKeyID: "test-key", TokenIssuer: "test-issuer", TokenAudience: "test-audience"})
	if err != nil {
		t.Fatalf("consent issuer: %v", err)
	}
	store := NewStore()
	client := newRegisterLogbook(logbook.Client(t, "consent-register"))
	server := httptest.NewServer(newMux(store, issuer, client))
	t.Cleanup(server.Close)
	return server.URL, store, client
}

// drain delivers everything the outbox holds, so the fake logbook sees it.
func drain(t *testing.T, client *registerLogbook, store ConsentStore) {
	t.Helper()
	if err := client.deliverOutbox(t.Context(), store); err != nil {
		t.Fatalf("deliver outbox: %v", err)
	}
}

func allGBOActivities() []string {
	return []string{
		consentGrantActivity, consentRevokeActivity,
		consentStatusActivity, consentListActivity, "gbo-overig@v1",
	}
}

func grant(t *testing.T, url string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"pi":                 "PI-abc123",
		"subject_ref":        testSubjectRef,
		"dienstverlener_oin": "00000001234567890000",
		"scopes":             []string{"bd:ib:2025"},
		"use_case":           "hypotheek",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	response, err := http.Post(url+"/consents", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post consent: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var created struct {
		ConsentID string `json:"consent_id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return created.ConsentID
}

// Recording a consent is a Dataverwerking of the voorziening, named by the
// portal-scoped reference the register stores — never the PI, which exists
// here only inside the signed token.
func TestGrantingAConsentIsLogged(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url, store, client := registerUnderTest(t, logbook)

	consentID := grant(t, url)

	drain(t, client, store)
	records := logbook.Written()
	if len(records) != 1 {
		t.Fatalf("wrote %d records, want 1: %+v", len(records), records)
	}
	record := records[0]
	if record.Name != "dataverwerking.toestemming-verlenen" {
		t.Errorf("name = %q", record.Name)
	}
	if got := record.Attributes[ldv.AttrProcessingActivityID]; got != consentGrantActivity {
		t.Errorf("processing_activity_id = %v, want %s", got, consentGrantActivity)
	}
	if got := record.Attributes[ldv.AttrDataSubjectID]; got != testSubjectRef {
		t.Errorf("data_subject_id = %v, want the portal-scoped reference", got)
	}
	if got := record.Attributes[ldv.AttrDataSubjectIDType]; got != ldvSubjectTypePortalSubject {
		t.Errorf("data_subject_id_type = %v, want %s", got, ldvSubjectTypePortalSubject)
	}
	if got := record.Attributes["gbo.consent.id"]; got != consentID {
		t.Errorf("gbo.consent.id = %v, want %s", got, consentID)
	}
	// The PI is authorization material for the dienstverlener, not an
	// identifier this register may write down.
	encoded, _ := json.Marshal(record)
	if strings.Contains(string(encoded), "PI-abc123") {
		t.Errorf("the PI leaked into the record: %s", encoded)
	}
}

// The status check is what makes a revocation take effect, so it is a
// processing in its own right rather than a read-only lookup.
func TestStatusAndRevocationAreLogged(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url, store, client := registerUnderTest(t, logbook)

	consentID := grant(t, url)

	response, err := http.Get(url + "/consents/" + consentID + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status endpoint = %d", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodDelete, url+"/consents/"+consentID, nil)
	if err != nil {
		t.Fatalf("build delete: %v", err)
	}
	revoked, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_ = revoked.Body.Close()
	if revoked.StatusCode != http.StatusOK {
		t.Fatalf("revoke = %d", revoked.StatusCode)
	}

	drain(t, client, store)
	records := logbook.Written()
	if len(ldvtest.ByName(records, "dataverwerking.toestemming-status")) != 1 {
		t.Errorf("expected one status record, got %+v", records)
	}
	revocations := ldvtest.ByName(records, "dataverwerking.toestemming-intrekken")
	if len(revocations) != 1 {
		t.Fatalf("expected one revocation record, got %+v", records)
	}
	if got := revocations[0].Attributes[ldv.AttrProcessingActivityID]; got != consentRevokeActivity {
		t.Errorf("processing_activity_id = %v, want %s", got, consentRevokeActivity)
	}
	if got := revocations[0].Attributes["gbo.consent.status"]; got != "REVOKED" {
		t.Errorf("gbo.consent.status = %v, want REVOKED", got)
	}
	// Every record of one citizen's consent names the same Betrokkene.
	for _, record := range records {
		if got := record.Attributes[ldv.AttrDataSubjectID]; got != testSubjectRef {
			t.Errorf("record %q names %v", record.Name, got)
		}
	}
}

// Citizen inzage is a processing too.
func TestListingACitizensConsentsIsLogged(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url, store, client := registerUnderTest(t, logbook)
	grant(t, url)

	response, err := http.Get(url + "/consents?subject_ref=" + testSubjectRef)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	_ = response.Body.Close()

	drain(t, client, store)
	listings := ldvtest.ByName(logbook.Written(), "dataverwerking.toestemming-inzage")
	if len(listings) != 1 {
		t.Fatalf("expected one inzage record, got %+v", logbook.Written())
	}
	if got := listings[0].Attributes["gbo.consent.count"]; got != float64(1) {
		t.Errorf("gbo.consent.count = %v, want 1", got)
	}
}

// An unscoped listing would read every citizen's consents — a Dataverwerking
// about all of them at once, which cannot be logged against a single
// Betrokkene. Rather than log it wrongly or not at all, the route refuses it.
func TestAnUnscopedListingIsRefused(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url, _, _ := registerUnderTest(t, logbook)
	grant(t, url)

	before := len(logbook.Written())
	response, err := http.Get(url + "/consents")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if got := len(logbook.Written()); got != before {
		t.Fatalf("a refused listing wrote %d records", got-before)
	}
}

// The outbox is what makes the mutation and its record atomic, so a logbook
// that is down no longer fails the citizen's action — and no longer loses the
// record either. Both halves are asserted, because either alone would be the
// bug this replaced.
func TestAConsentSurvivesALogbookOutageAndIsLoggedAfterwards(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url, store, client := registerUnderTest(t, logbook)
	logbook.RefuseEverything()

	consentID := grant(t, url)
	if consentID == "" {
		t.Fatal("the consent should be created even though the logbook is refusing")
	}

	// Nothing reached the logbook, and delivery reports that.
	if err := client.deliverOutbox(t.Context(), store); err == nil {
		t.Fatal("delivery should fail while the logbook refuses")
	}
	if len(logbook.Written()) != 0 {
		t.Fatalf("logbook accepted %d records while refusing", len(logbook.Written()))
	}
	// But the record is not lost: it is in the outbox, committed with the
	// consent it describes.
	pending, err := store.PendingRecords(t.Context(), 10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("outbox holds %d records, want the consent's own", len(pending))
	}
}

// The transactional guarantee, from the other side: a store that refuses the
// write leaves neither a consent nor a record.
func TestAFailedStoreLeavesNeitherConsentNorRecord(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	issuer, err := NewConsentIssuer(config{SigningKeyID: "test-key", TokenIssuer: "test-issuer", TokenAudience: "test-audience"})
	if err != nil {
		t.Fatalf("consent issuer: %v", err)
	}
	store := &failingStore{Store: NewStore()}
	client := newRegisterLogbook(logbook.Client(t, "consent-register"))
	server := httptest.NewServer(newMux(store, issuer, client))
	defer server.Close()

	body, err := json.Marshal(map[string]any{
		"pi": "PI-abc123", "subject_ref": testSubjectRef,
		"dienstverlener_oin": "00000001234567890000", "scopes": []string{"bd:ib:2025"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	response, err := http.Post(server.URL+"/consents", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	pending, err := store.PendingRecords(t.Context(), 10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("outbox holds %d records for a consent that was never stored", len(pending))
	}
}

// failingStore refuses to create, so the atomicity can be tested from the
// failing side.
type failingStore struct {
	*Store
}

func (f *failingStore) Create(context.Context, *Consent, []byte) error {
	return errors.New("storage is down")
}

// A status query for a consent that does not exist touched nobody's data.
func TestAMissingConsentLogsNothing(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url, store, client := registerUnderTest(t, logbook)

	response, err := http.Get(url + "/consents/c-does-not-exist/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
	drain(t, client, store)
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("wrote %d records for a consent that does not exist: %+v", len(records), records)
	}
}
