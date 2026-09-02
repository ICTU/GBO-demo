package main

import (
	"bytes"
	"encoding/json"
	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The portal-scoped reference this register works with. It is a pseudonym,
// derived by the portal; the register never sees the BSN behind it.
const testSubjectRef = "EP-3f9a1c77b2"

func registerUnderTest(t *testing.T, logbook *ldvtest.Logbook) string {
	t.Helper()
	issuer, err := NewConsentIssuer(config{SigningKeyID: "test-key", TokenIssuer: "test-issuer", TokenAudience: "test-audience"})
	if err != nil {
		t.Fatalf("consent issuer: %v", err)
	}
	client := newRegisterLogbook(logbook.Client(t, "consent-register"))
	server := httptest.NewServer(newMux(NewStore(), issuer, client))
	t.Cleanup(server.Close)
	return server.URL
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
	url := registerUnderTest(t, logbook)

	consentID := grant(t, url)

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
	url := registerUnderTest(t, logbook)

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
	url := registerUnderTest(t, logbook)
	grant(t, url)

	response, err := http.Get(url + "/consents?subject_ref=" + testSubjectRef)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	_ = response.Body.Close()

	listings := ldvtest.ByName(logbook.Written(), "dataverwerking.toestemming-inzage")
	if len(listings) != 1 {
		t.Fatalf("expected one inzage record, got %+v", logbook.Written())
	}
	if got := listings[0].Attributes["gbo.consent.count"]; got != float64(1) {
		t.Errorf("gbo.consent.count = %v, want 1", got)
	}
}

// A listing without a subject_ref is an operational query rather than inzage
// by a Betrokkene: it names nobody, so there is nothing to log.
func TestAnUnscopedListingLogsNothing(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := registerUnderTest(t, logbook)
	grant(t, url)

	before := len(logbook.Written())
	response, err := http.Get(url + "/consents")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	_ = response.Body.Close()
	if got := len(logbook.Written()); got != before {
		t.Fatalf("an unscoped listing wrote %d extra records", got-before)
	}
}

// Fail-closed: a consent whose record the logbook refuses is not confirmed to
// the caller.
func TestAGrantThatCannotBeLoggedIsRefused(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := registerUnderTest(t, logbook)
	logbook.RefuseEverything()

	body, err := json.Marshal(map[string]any{
		"pi":                 "PI-abc123",
		"subject_ref":        testSubjectRef,
		"dienstverlener_oin": "00000001234567890000",
		"scopes":             []string{"bd:ib:2025"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	response, err := http.Post(url+"/consents", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	var problem map[string]string
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(problem["error"], "could not be logged") {
		t.Errorf("error = %q", problem["error"])
	}
}

// A status query for a consent that does not exist touched nobody's data.
func TestAMissingConsentLogsNothing(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := registerUnderTest(t, logbook)

	response, err := http.Get(url + "/consents/c-does-not-exist/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("wrote %d records for a consent that does not exist: %+v", len(records), records)
	}
}
