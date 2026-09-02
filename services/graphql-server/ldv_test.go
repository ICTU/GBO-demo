package main

import (
	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
)

const (
	activity2025 = "bd-ib-2025@v1"
	activity2024 = "bd-ib-2024@v1"
	// The BSN the demo mock data is keyed on. No record may contain it.
	demoBSN = "123456789"
)

// sourceUnderTest wires the GraphQL server with a logbook attached.
func sourceUnderTest(t *testing.T, logbook *ldvtest.Logbook) string {
	t.Helper()
	store, err := loadMockData("mockdata/citizens.json")
	if err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("graphql-server-test")
	schema, err := buildSchema(tracer, store)
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	logbookForBron := newSourceLogbook(
		logbook.Client(t, "graphql-server"),
		ldvQueryConfig{YearActivityTemplate: "bd-ib-%d@v1"},
	)
	server := httptest.NewServer(newMux(&schema, tracer, logbookForBron))
	t.Cleanup(server.Close)
	return server.URL
}

func queryYear(t *testing.T, url string, headers map[string]string, body string) *http.Response {
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

const singleYearQuery = `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){heeftBelastingjaarAangifte(belastingjaren:[2025]){belastingjaar}}}","variables":{"bsn":"` + demoBSN + `"}}`

// The sidecar hands the source the trace metadata and the pseudonym it used.
// The source's record must join that tree and name the same Betrokkene the
// same way, or the two halves of one request cannot be read together.
func TestSourceQueryLogsUnderTheSidecarsTraceAndSubject(t *testing.T) {
	logbook := ldvtest.New(t, activity2025, activity2024)
	url := sourceUnderTest(t, logbook)

	response := queryYear(t, url, map[string]string{
		ldv.HeaderTraceID:       "0af7651916cd43dd8448eb211c80319c",
		ldv.HeaderParentSpanID:  "b7ad6b7169203331",
		ldv.HeaderSubjectID:     "PI-abc123",
		ldv.HeaderSubjectIDType: ldv.SubjectTypePI,
		"X-GBO-Scope":           "bd:ib:2025",
	}, singleYearQuery)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	records := logbook.Written()
	if len(records) != 1 {
		t.Fatalf("wrote %d records, want 1: %+v", len(records), records)
	}
	record := records[0]
	if record.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace_id = %q", record.TraceID)
	}
	if record.ParentSpanID != "b7ad6b7169203331" {
		t.Errorf("parent_span_id = %q, want the sidecar's forward span", record.ParentSpanID)
	}
	if record.SpanID == record.ParentSpanID {
		t.Error("a record must have its own span id")
	}
	if got := record.Attributes[ldv.AttrDataSubjectID]; got != "PI-abc123" {
		t.Errorf("data_subject_id = %v, want the PI the sidecar passed on", got)
	}
	// The scope the consumer was authorized for selects the register entry
	// generated from that same scope definition.
	if got := record.Attributes[ldv.AttrProcessingActivityID]; got != activity2025 {
		t.Errorf("processing_activity_id = %v, want %s", got, activity2025)
	}
	ldvtest.AssertNoBSN(t, records, demoBSN)
}

// The EUDI flow carries no scope header and no pseudonym: the source holds
// only a BSN and must derive a logbook-local reference from it.
func TestSourceQueryWithoutSidecarMetadataUsesALocalPseudonymAndTheYear(t *testing.T) {
	logbook := ldvtest.New(t, activity2025, activity2024)
	url := sourceUnderTest(t, logbook)

	if response := queryYear(t, url, nil, singleYearQuery); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	records := logbook.Written()
	if len(records) != 1 {
		t.Fatalf("wrote %d records, want 1: %+v", len(records), records)
	}
	if got := records[0].Attributes[ldv.AttrDataSubjectIDType]; got != ldv.SubjectTypePseudonym {
		t.Errorf("data_subject_id_type = %v, want %s", got, ldv.SubjectTypePseudonym)
	}
	// No scope header, so the belastingjaar decides which activity applies.
	if got := records[0].Attributes[ldv.AttrProcessingActivityID]; got != activity2025 {
		t.Errorf("processing_activity_id = %v, want %s", got, activity2025)
	}
	ldvtest.AssertNoBSN(t, records, demoBSN)
}

// Two belastingjaren are two verwerkingsactiviteiten, so one query yields two
// records for the same Betrokkene — siblings, each naming its own activity.
func TestAMultiYearQueryLogsOneRecordPerVerwerkingsactiviteit(t *testing.T) {
	logbook := ldvtest.New(t, activity2025, activity2024)
	url := sourceUnderTest(t, logbook)

	body := `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){heeftBelastingjaarAangifte(belastingjaren:[2024,2025]){belastingjaar}}}","variables":{"bsn":"` + demoBSN + `"}}`
	if response := queryYear(t, url, nil, body); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	records := logbook.Written()
	if len(records) != 2 {
		t.Fatalf("wrote %d records, want 2: %+v", len(records), records)
	}
	activities := map[any]bool{}
	subjects := map[any]bool{}
	for _, record := range records {
		activities[record.Attributes[ldv.AttrProcessingActivityID]] = true
		subjects[record.Attributes[ldv.AttrDataSubjectID]] = true
	}
	if !activities[activity2024] || !activities[activity2025] {
		t.Errorf("activities = %v, want both years", activities)
	}
	if len(subjects) != 1 {
		t.Errorf("one Betrokkene should yield one subject id, got %v", subjects)
	}
	ldvtest.AssertNoBSN(t, records, demoBSN)
}

// A year the register does not describe cannot be logged, and therefore is
// not served. That is the register requirement biting: a verstrekking whose
// verwerkingsactiviteit nobody wrote down is one nobody can account for
// afterwards, so it does not happen.
func TestAnUndescribedYearIsRefused(t *testing.T) {
	logbook := ldvtest.New(t, activity2025)
	url := sourceUnderTest(t, logbook)

	body := `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){heeftBelastingjaarAangifte(belastingjaren:[2024]){belastingjaar}}}","variables":{"bsn":"` + demoBSN + `"}}`
	response := queryYear(t, url, nil, body)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("the logbook accepted a record it should have refused: %+v", records)
	}
}

// Fail-closed at the source too: the answer is buffered until the records are
// confirmed, so a refused write withholds the data rather than trailing it.
func TestAQueryThatCannotBeLoggedWithholdsTheData(t *testing.T) {
	logbook := ldvtest.New(t, activity2025)
	url := sourceUnderTest(t, logbook)
	logbook.RefuseEverything()

	response := queryYear(t, url, nil, singleYearQuery)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), "belastingjaar") {
		t.Fatalf("query data leaked despite the refused record: %s", body)
	}
}

// An introspection query resolves no Betrokkene and therefore processes no
// personal data.
func TestAnIntrospectionQueryWritesNoRecord(t *testing.T) {
	logbook := ldvtest.New(t, activity2025)
	url := sourceUnderTest(t, logbook)

	if response := queryYear(t, url, nil, `{"query":"{__schema{queryType{name}}}"}`); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("wrote %d records for an introspection query: %+v", len(records), records)
	}
}

// A BSN the bron does not serve resolves nothing, so there is no Betrokkene
// to file a record about.
func TestAnUnknownSubjectWritesNoRecord(t *testing.T) {
	logbook := ldvtest.New(t, activity2025)
	url := sourceUnderTest(t, logbook)

	body := `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"999999990"}}`
	if response := queryYear(t, url, nil, body); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("wrote %d records for an unknown subject: %+v", len(records), records)
	}
}

// A query covering two years is two verstrekkingen, and each record has to
// name the activity its own belastingjaar belongs to. The scope header can
// only describe one of them, so preferring it made the 2024 record claim the
// 2025 entry — the kind of mislabelling that only shows up once a real
// multi-year request runs.
func TestEachYearGetsItsOwnActivityEvenUnderOneScope(t *testing.T) {
	logbook := ldvtest.New(t, activity2025, activity2024)
	url := sourceUnderTest(t, logbook)

	body := `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){heeftBelastingjaarAangifte(belastingjaren:[2024,2025]){belastingjaar}}}","variables":{"bsn":"` + demoBSN + `"}}`
	// The consumer was authorized under one scope, which names 2025 only.
	if response := queryYear(t, url, map[string]string{"X-GBO-Scope": "bd:ib:2025"}, body); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	records := logbook.Written()
	if len(records) != 2 {
		t.Fatalf("wrote %d records, want one per year: %+v", len(records), records)
	}
	for _, record := range records {
		year, _ := record.Attributes["gbo.belastingjaar"].(float64)
		activity, _ := record.Attributes[ldv.AttrProcessingActivityID].(string)
		want := "bd-ib-" + strconv.Itoa(int(year)) + "@v1"
		if activity != want {
			t.Errorf("a record for %d names %q, want %q — the activity must match its own year", int(year), activity, want)
		}
	}
}
