package main

import (
	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
)

const (
	// The nabestaande in the demo data: Jansen, whose partner died. Their BSN
	// may not appear in any record.
	nabestaandeBSN = "999991772"
	// A BSN the bron serves that has no akte van overlijden.
	geenAkteBSN = "123456789"
)

func brpUnderTest(t *testing.T, logbook *ldvtest.Logbook) string {
	t.Helper()
	store, err := loadMockData("mockdata/personen.json")
	if err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("brp-graphql-server-test")
	schema, err := buildSchema(tracer, store)
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	server := httptest.NewServer(newMux(&schema, tracer, newSourceLogbook(logbook.Client(t, "brp-graphql-server")), nil))
	t.Cleanup(server.Close)
	return server.URL
}

func brpQuery(t *testing.T, url string, headers map[string]string, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url+"/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

const akteQuery = `{"query":"query($bsn: BSN!){akteVanOverlijden(bsn:$bsn){datum_overlijden overledene_ouders}}","variables":{"bsn":"` + nabestaandeBSN + `"}}`

// The certificate is about more people than the one who asked for it. Each
// living Betrokkene gets a record, and the further ones hang under the
// requester's because they exist only as part of that one processing.
func TestAkteVanOverlijdenLogsARecordPerBetrokkene(t *testing.T) {
	logbook := ldvtest.New(t, akteActivity, persoonsgegevensActivity)
	url := brpUnderTest(t, logbook)

	if response := brpQuery(t, url, nil, akteQuery); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	records := logbook.Written()
	primary := ldvtest.ByName(records, "dataverwerking.bronbevraging")
	children := ldvtest.ByName(records, "dataverwerking.bronbevraging.medebetrokkene")
	if len(primary) != 1 {
		t.Fatalf("expected one record for the aanvrager, got %d: %+v", len(primary), records)
	}
	// The demo akte names both parents of the deceased, and both are living.
	if len(children) != 2 {
		t.Fatalf("expected a child record per living relative, got %d: %+v", len(children), records)
	}
	for _, child := range children {
		if child.ParentSpanID != primary[0].SpanID {
			t.Errorf("child record %q does not hang under the aanvrager's record", child.SpanID)
		}
		if got := child.Attributes[ldv.AttrDataSubjectIDType]; got != ldvSubjectTypeBRPPersoonID {
			t.Errorf("child data_subject_id_type = %v, want %s", got, ldvSubjectTypeBRPPersoonID)
		}
		if got := child.Attributes[ldv.AttrProcessingActivityID]; got != akteActivity {
			t.Errorf("child processing_activity_id = %v, want %s", got, akteActivity)
		}
		if got := child.Attributes["gbo.betrokkene.rol"]; got != "ouder-van-overledene" {
			t.Errorf("child rol = %v", got)
		}
	}
	if got := primary[0].Attributes["gbo.betrokkene.rol"]; got != "aanvrager" {
		t.Errorf("primary rol = %v, want aanvrager", got)
	}
	// The deceased's data is disclosed while the deceased is not a Betrokkene.
	// The record says so, rather than leaving it looking like an omission.
	if got := primary[0].Attributes["gbo.akte.overledene_verwerkt"]; got != true {
		t.Errorf("gbo.akte.overledene_verwerkt = %v, want true", got)
	}
	// Every child names a distinct person.
	subjects := map[any]bool{}
	for _, child := range children {
		subjects[child.Attributes[ldv.AttrDataSubjectID]] = true
	}
	if len(subjects) != len(children) {
		t.Errorf("child records name %d distinct Betrokkenen for %d records", len(subjects), len(children))
	}
	ldvtest.AssertNoBSN(t, records, nabestaandeBSN)
}

// The BRP chain is EUDI-only today, so the request arrives with a BSN and the
// bron has to derive a logbook-local reference for the requester.
func TestAkteRequesterIsNamedByALocalPseudonym(t *testing.T) {
	logbook := ldvtest.New(t, akteActivity, persoonsgegevensActivity)
	url := brpUnderTest(t, logbook)

	brpQuery(t, url, nil, akteQuery)

	primary := ldvtest.ByName(logbook.Written(), "dataverwerking.bronbevraging")
	if len(primary) != 1 {
		t.Fatalf("expected one primary record, got %d", len(primary))
	}
	if got := primary[0].Attributes[ldv.AttrDataSubjectIDType]; got != ldv.SubjectTypePseudonym {
		t.Errorf("data_subject_id_type = %v, want %s", got, ldv.SubjectTypePseudonym)
	}
	subject, _ := primary[0].Attributes[ldv.AttrDataSubjectID].(string)
	if !strings.HasPrefix(subject, "LP-") {
		t.Errorf("data_subject_id = %q, want a logbook-local pseudonym", subject)
	}
}

// A sidecar in front of a pseudonym contract passes the PI on; the bron must
// then name the requester the same way its sidecar did.
func TestAkteRequesterKeepsTheSidecarsPseudonym(t *testing.T) {
	logbook := ldvtest.New(t, akteActivity, persoonsgegevensActivity)
	url := brpUnderTest(t, logbook)

	brpQuery(t, url, map[string]string{
		ldv.HeaderTraceID:       "0af7651916cd43dd8448eb211c80319c",
		ldv.HeaderParentSpanID:  "b7ad6b7169203331",
		ldv.HeaderSubjectID:     "PI-abc123",
		ldv.HeaderSubjectIDType: ldv.SubjectTypePI,
	}, akteQuery)

	primary := ldvtest.ByName(logbook.Written(), "dataverwerking.bronbevraging")
	if len(primary) != 1 {
		t.Fatalf("expected one primary record, got %d", len(primary))
	}
	if got := primary[0].Attributes[ldv.AttrDataSubjectID]; got != "PI-abc123" {
		t.Errorf("data_subject_id = %v, want the PI the sidecar passed on", got)
	}
	if primary[0].ParentSpanID != "b7ad6b7169203331" {
		t.Errorf("parent_span_id = %q, want the sidecar's forward span", primary[0].ParentSpanID)
	}
	for _, record := range logbook.Written() {
		if record.TraceID != "0af7651916cd43dd8448eb211c80319c" {
			t.Errorf("record %q has trace_id %q", record.Name, record.TraceID)
		}
	}
}

// The other query entry point is an ordinary single-subject verstrekking.
func TestIngeschrevenPersoonLogsOneRecord(t *testing.T) {
	logbook := ldvtest.New(t, akteActivity, persoonsgegevensActivity)
	url := brpUnderTest(t, logbook)

	body := `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){ ... on Ingezetene { geslachtsnaam } }}","variables":{"bsn":"` + geenAkteBSN + `"}}`
	if response := brpQuery(t, url, nil, body); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	records := logbook.Written()
	if len(records) != 1 {
		t.Fatalf("wrote %d records, want 1: %+v", len(records), records)
	}
	if got := records[0].Attributes[ldv.AttrProcessingActivityID]; got != persoonsgegevensActivity {
		t.Errorf("processing_activity_id = %v, want %s", got, persoonsgegevensActivity)
	}
	if ldvtest.ByName(records, "dataverwerking.bronbevraging.medebetrokkene") != nil {
		t.Error("a single-subject query must not produce medebetrokkene records")
	}
	ldvtest.AssertNoBSN(t, records, geenAkteBSN)
}

// A person without a deceased partner has no akte. The query still ran
// against their record, so the requester is a Betrokkene — but there is no
// certificate and therefore no further Betrokkenen.
func TestAQueryWithoutAnAkteLogsOnlyTheRequester(t *testing.T) {
	logbook := ldvtest.New(t, akteActivity, persoonsgegevensActivity)
	url := brpUnderTest(t, logbook)

	body := `{"query":"query($bsn: BSN!){akteVanOverlijden(bsn:$bsn){datum_overlijden}}","variables":{"bsn":"` + geenAkteBSN + `"}}`
	if response := brpQuery(t, url, nil, body); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("wrote %d records for a query that disclosed nothing: %+v", len(records), records)
	}
}

// Fail-closed here too, and the child records make it stricter: the answer is
// withheld unless every Betrokkene's record landed.
func TestAnAkteThatCannotBeFullyLoggedWithholdsTheData(t *testing.T) {
	logbook := ldvtest.New(t, akteActivity, persoonsgegevensActivity)
	url := brpUnderTest(t, logbook)
	logbook.RefuseEverything()

	response := brpQuery(t, url, nil, akteQuery)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), "datum_overlijden") {
		t.Fatalf("certificate data leaked despite the refused record: %s", body)
	}
}

func TestAnIntrospectionQueryWritesNoRecord(t *testing.T) {
	logbook := ldvtest.New(t, akteActivity, persoonsgegevensActivity)
	url := brpUnderTest(t, logbook)

	if response := brpQuery(t, url, nil, `{"query":"{__schema{queryType{name}}}"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("wrote %d records for an introspection query: %+v", len(records), records)
	}
}

// A relative who has died is no longer a Betrokkene, so no record is written
// about them — the rule the akte's child records are selected by.
func TestOnlyLivingRelativesBecomeBetrokkenen(t *testing.T) {
	overleden := "2020-01-01"
	overledene := NatuurlijkPersoon{HeeftOuder: []NatuurlijkPersoon{
		{ID: "018f2c4a-0000-0000-0000-00000000000a"},
		{ID: "018f2c4a-0000-0000-0000-00000000000b", DatumOverlijden: &overleden},
		{Geslachtsnaam: "Zonder-Id"},
	}}
	relatives := levendeBetrokkenenInAkte(overledene)
	if len(relatives) != 1 || relatives[0].id != "018f2c4a-0000-0000-0000-00000000000a" {
		t.Fatalf("relatives = %+v, want only the living, identifiable parent", relatives)
	}
}
