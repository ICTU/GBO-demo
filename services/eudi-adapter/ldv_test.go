package main

import (
	"context"
	"encoding/json"
	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The BSN in the demo PID disclosure. It may not appear in any record.
const walletBSN = "123456789"

// sourceAnswer is what the stub source returns for the demo subject.
const sourceAnswer = `{"data": {"ingeschrevenPersoon": {"heeftBelastingjaarAangifte": [{
	"belastingjaar": 2025,
	"status": "Definitief vastgesteld",
	"indieningsdatum": "2026-04-01",
	"verzamelinkomen": {"waarde": 43000.0, "valuta": "EUR"},
	"box1Inkomen": {"waarde": 41000.0, "valuta": "EUR"},
	"box2Inkomen": {"waarde": 1000.0, "valuta": "EUR"},
	"box3Inkomen": {"waarde": 1000.0, "valuta": "EUR"}
}]}}}`

// capturingOutway answers like the source and keeps the headers that reached
// it, for tests about what the adapter hands the bronhouder.
func capturingOutway(received *http.Header) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*received = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sourceAnswer))
	}
}

// issuanceUnderTest wires the adapter against a stub metadata publisher and a
// stub Outway, with a logbook attached, and returns the adapter's URL.
func issuanceUnderTest(t *testing.T, logbook *ldvtest.Logbook) string {
	t.Helper()
	var discarded http.Header
	return issuanceUnderTestWithOutway(t, logbook, capturingOutway(&discarded))
}

// issuanceUnderTestWithOutway is issuanceUnderTest with a caller-supplied
// Outway.
func issuanceUnderTestWithOutway(t *testing.T, logbook *ldvtest.Logbook, outwayHandler http.HandlerFunc) string {
	t.Helper()
	metadataPayload, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatalf("read shipped source metadata: %v", err)
	}
	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_, _ = w.Write(metadataPayload)
	}))
	t.Cleanup(metadataServer.Close)

	metadata, err := fetchSourceMetadataForTest(context.Background(), http.DefaultClient, testSourceMetadataConfig{
		URL: metadataServer.URL + "/metadata/.well-known/gbo", MetadataTransport: sourceTransportFSC,
		DataTransport: sourceTransportFSC, SourceID: "belastingdienst",
		ExpectedOIN: "99999999900000000200", TypeID: "inkomensverklaring",
	})
	if err != nil {
		t.Fatalf("load source metadata: %v", err)
	}

	outway := httptest.NewServer(outwayHandler)
	t.Cleanup(outway.Close)

	cfg := config{
		Port: "0", OutwayURL: outway.URL, SourceDataTransport: sourceTransportFSC,
		SourceDataFSCServiceReference: "bri", SourceDataFSCGrantHash: "data-grant",
	}
	client := newIssuanceLogbook(logbook.Client(t, "eudi-adapter"), map[string]string{"belastingdienst": "https://logboek.belastingdienst.nl/data-processing-operations"})
	server := httptest.NewServer(withFscTraceContext(testMux(cfg, http.DefaultClient, metadata, client)))
	t.Cleanup(server.Close)
	return server.URL
}

const pidDisclosure = `[{
	"id": "req-1",
	"attestations": [{
		"attestation_type": "urn:eudi:pid:nl:1",
		"attributes": {"urn:eudi:pid:nl:1": {"bsn": "` + walletBSN + `"}}
	}]
}]`

func issue(t *testing.T, url string) *http.Response {
	t.Helper()
	response, err := http.Post(url+"/attestations/belastingdienst/inkomensverklaring?jaar=2025",
		"application/json", strings.NewReader(pidDisclosure))
	if err != nil {
		t.Fatalf("post issuance request: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func allGBOActivities() []string {
	return []string{pidExtractionActivity, attestationBuildActivity}
}

// One issuance is two Dataverwerkingen of the voorziening: reading the BSN
// out of the PID, and assembling the attestation from what the source
// returned. Both are logged, and the second hangs under the first because it
// only exists once the first established whose attestation this is.
func TestAnIssuanceLogsBothDataverwerkingen(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := issuanceUnderTest(t, logbook)

	if response := issue(t, url); response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, raw)
	}

	records := logbook.Written()
	extraction := ldvtest.ByName(records, "dataverwerking.pid-bsn-extractie")
	assembly := ldvtest.ByName(records, "dataverwerking.attestatie-samenstellen")
	if len(extraction) != 1 || len(assembly) != 1 {
		t.Fatalf("expected one record of each kind, got %+v", records)
	}
	if assembly[0].ParentSpanID != extraction[0].SpanID {
		t.Error("the assembly record should hang under the PID extraction")
	}
	if extraction[0].TraceID != assembly[0].TraceID {
		t.Error("both records of one issuance must share a trace id")
	}
	if got := extraction[0].Attributes[ldv.AttrProcessingActivityID]; got != pidExtractionActivity {
		t.Errorf("extraction processing_activity_id = %v, want %s", got, pidExtractionActivity)
	}
	if got := assembly[0].Attributes[ldv.AttrProcessingActivityID]; got != attestationBuildActivity {
		t.Errorf("assembly processing_activity_id = %v, want %s", got, attestationBuildActivity)
	}
	// The record says how much was processed, not what: it is a record about
	// the attestation, not a copy of it.
	if got := assembly[0].Attributes["dpl.gbo.attestatieClaims"]; got == nil || got == float64(0) {
		t.Errorf("dpl.gbo.attestatieClaims = %v, want the number of claims", got)
	}
	if got := assembly[0].Attributes["dpl.gbo.sourceId"]; got != "belastingdienst" {
		t.Errorf("dpl.gbo.sourceId = %v", got)
	}
}

// The adapter holds a BSN and nothing else, so it derives a logbook-local
// pseudonym. Both records name the same Betrokkene, and neither names them by
// the BSN.
func TestIssuanceRecordsNameTheHolderWithoutTheBSN(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := issuanceUnderTest(t, logbook)

	issue(t, url)

	records := logbook.Written()
	if len(records) != 2 {
		t.Fatalf("wrote %d records, want 2: %+v", len(records), records)
	}
	subjects := map[any]bool{}
	for _, record := range records {
		if got := record.Attributes[ldv.AttrDataSubjectIDType]; got != ldv.SubjectTypePseudonym {
			t.Errorf("record %q data_subject_id_type = %v, want %s", record.Name, got, ldv.SubjectTypePseudonym)
		}
		subject, _ := record.Attributes[ldv.AttrDataSubjectID].(string)
		if !strings.HasPrefix(subject, "LP-") {
			t.Errorf("record %q data_subject_id = %q, want a logbook-local pseudonym", record.Name, subject)
		}
		subjects[subject] = true
	}
	if len(subjects) != 1 {
		t.Errorf("the two records of one issuance name %d different Betrokkenen", len(subjects))
	}
	ldvtest.AssertNoBSN(t, records, walletBSN)
}

// Without a caller's trace the adapter ties its trace to the
// Fsc-Transaction-Id it mints, so a request that crosses FSC once carries one
// value in the txlog, the decision log and every logbook.
func TestIssuanceRecordsCarryTheFscTransactionID(t *testing.T) {
	var received http.Header
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := issuanceUnderTestWithOutway(t, logbook, capturingOutway(&received))

	issue(t, url)

	want := ldv.NormalizeTraceID(received.Get("Fsc-Transaction-Id"))
	if want == "" {
		t.Fatalf("the source received no usable Fsc-Transaction-Id: %q", received.Get("Fsc-Transaction-Id"))
	}
	for _, record := range logbook.Written() {
		if record.TraceID != want {
			t.Errorf("record %q trace_id = %q, want the transaction id %q", record.Name, record.TraceID, want)
		}
	}
}

// The source files its records under the action that called it: the assembly
// record, whose nextLogbookId points at the bronhouder. So the span on the
// traceparent it receives has to be that record's, one a reader of this
// logbook can resolve.
func TestTheSourceHangsUnderTheAssemblyRecord(t *testing.T) {
	var received http.Header
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := issuanceUnderTestWithOutway(t, logbook, capturingOutway(&received))

	issue(t, url)

	assembly := ldvtest.ByName(logbook.Written(), "dataverwerking.attestatie-samenstellen")
	if len(assembly) != 1 {
		t.Fatalf("expected one assembly record, got %+v", logbook.Written())
	}
	if got := ldv.TraceContextFrom(context.Background(), received, "").TraceID; got != assembly[0].TraceID {
		t.Errorf("the source received trace %q, want the adapter's %q", got, assembly[0].TraceID)
	}
	if got := ldv.ParentSpanFor(received, assembly[0].TraceID); got != assembly[0].SpanID {
		t.Errorf("the source would hang under span %q, want the assembly record's %q", got, assembly[0].SpanID)
	}
}

// A caller that brings its own traceparent — an issuance-server with OTel —
// is followed, not overruled (§3.3.1): the records take its trace id over, the
// extraction hangs under its span, and the source receives the same trace. The
// Fsc-Transaction-Id the adapter mints stays FSC's own.
func TestACallersTraceIsTakenOver(t *testing.T) {
	const callersTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	const callersSpan = "00f067aa0ba902b7"

	var received http.Header
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := issuanceUnderTestWithOutway(t, logbook, capturingOutway(&received))

	request, err := http.NewRequest(http.MethodPost,
		url+"/attestations/belastingdienst/inkomensverklaring?jaar=2025",
		strings.NewReader(pidDisclosure))
	if err != nil {
		t.Fatalf("build issuance request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("traceparent", "00-"+callersTrace+"-"+callersSpan+"-01")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post issuance request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()

	written := logbook.Written()
	if len(written) == 0 {
		t.Fatal("no records written")
	}
	for _, record := range written {
		if record.TraceID != callersTrace {
			t.Errorf("record %q trace_id = %q, want the caller's trace %q", record.Name, record.TraceID, callersTrace)
		}
	}
	extraction := ldvtest.ByName(written, "dataverwerking.pid-bsn-extractie")
	if len(extraction) != 1 || extraction[0].ParentSpanID != callersSpan {
		t.Errorf("the extraction should hang under the caller's span %q, got %+v", callersSpan, extraction)
	}
	if got := ldv.TraceContextFrom(context.Background(), received, "").TraceID; got != callersTrace {
		t.Errorf("the source received trace %q, want the caller's %q", got, callersTrace)
	}
	if received.Get("Fsc-Transaction-Id") == "" {
		t.Error("the source received no Fsc-Transaction-Id; FSC needs its own id on every transaction")
	}
}

// Fail-closed, and early: the record of the PID extraction is filed before
// the source is called, so a request that cannot be logged never reaches the
// bronhouder.
func TestAnIssuanceThatCannotBeLoggedNeverReachesTheSource(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)

	var sourceCalls int
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls++
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer outway.Close()

	metadataPayload, err := os.ReadFile("../graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatalf("read shipped source metadata: %v", err)
	}
	metadataServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", sourceMetadataMediaType)
		_, _ = w.Write(metadataPayload)
	}))
	defer metadataServer.Close()
	metadata, err := fetchSourceMetadataForTest(context.Background(), http.DefaultClient, testSourceMetadataConfig{
		URL: metadataServer.URL + "/metadata/.well-known/gbo", MetadataTransport: sourceTransportFSC,
		DataTransport: sourceTransportFSC, SourceID: "belastingdienst",
		ExpectedOIN: "99999999900000000200", TypeID: "inkomensverklaring",
	})
	if err != nil {
		t.Fatalf("load source metadata: %v", err)
	}

	client := newIssuanceLogbook(logbook.Client(t, "eudi-adapter"), map[string]string{"belastingdienst": "https://logboek.belastingdienst.nl/data-processing-operations"})
	cfg := config{
		Port: "0", OutwayURL: outway.URL, SourceDataTransport: sourceTransportFSC,
		SourceDataFSCServiceReference: "bri", SourceDataFSCGrantHash: "data-grant",
	}
	server := httptest.NewServer(testMux(cfg, http.DefaultClient, metadata, client))
	defer server.Close()
	logbook.RefuseEverything()

	response := issue(t, server.URL)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	if sourceCalls != 0 {
		t.Fatalf("the source was called %d times despite the refused record", sourceCalls)
	}
}

// A disclosure without a BSN identifies nobody; the adapter rejects it before
// there is anything to log.
func TestADisclosureWithoutABSNLogsNothing(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := issuanceUnderTest(t, logbook)

	response, err := http.Post(url+"/attestations/belastingdienst/inkomensverklaring?jaar=2025",
		"application/json", strings.NewReader(`[{"id":"req-1","attestations":[{"attestation_type":"urn:eudi:pid:nl:1","attributes":{}}]}]`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("wrote %d records for a disclosure that names nobody: %+v", len(records), records)
	}
}

// NormalizeTraceID is what lets the transaction id serve as a trace id where
// no traceparent arrived. Its edge cases decide whether records correlate.
func TestNormalizeTraceID(t *testing.T) {
	cases := map[string]string{
		"0af76519-16cd-43dd-8448-eb211c80319c": "0af7651916cd43dd8448eb211c80319c",
		"0AF76519-16CD-43DD-8448-EB211C80319C": "0af7651916cd43dd8448eb211c80319c",
		" 0af7651916cd43dd8448eb211c80319c ":   "0af7651916cd43dd8448eb211c80319c",
		"not-a-uuid":                           "",
		"":                                     "",
		"0af7651916cd43dd8448eb211c80319":      "",
	}
	for input, want := range cases {
		if got := ldv.NormalizeTraceID(input); got != want {
			t.Errorf("ldv.NormalizeTraceID(%q) = %q, want %q", input, got, want)
		}
	}
}

// Serialising the record must never be the thing that leaks a BSN, so the
// guard is asserted against a record built the way the adapter builds one.
func TestAttributesDropEmptyValues(t *testing.T) {
	attributes := ldv.Attributes("gbo-overig@v1", "LP-abc", ldv.SubjectTypePseudonym, "", map[string]any{
		"gbo.present": "yes",
		"gbo.empty":   "",
		"gbo.nil":     nil,
	})
	if _, present := attributes[ldv.AttrForeignOperationProcessor]; present {
		t.Error("an absent foreign processor must not be written as an empty attribute")
	}
	if _, present := attributes["gbo.empty"]; present {
		t.Error("an empty attribute says nothing and should be dropped")
	}
	if _, present := attributes["gbo.nil"]; present {
		t.Error("a nil attribute should be dropped")
	}
	if attributes["gbo.present"] != "yes" {
		t.Error("a set attribute must survive")
	}
	encoded, err := json.Marshal(attributes)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), walletBSN) {
		t.Fatalf("attributes contain the BSN: %s", encoded)
	}
}

// The chain view is assembled by following pointers, not by one place holding
// everything — which is what LDV's per-Verantwoordelijke model requires. The
// issuance is the hop where GBO's processing hands off to a bronhouder, so it
// is the record that has to say where the rest was written down.
func TestTheAssemblyRecordPointsAtTheSourcesLogbook(t *testing.T) {
	logbook := ldvtest.New(t, pidExtractionActivity, attestationBuildActivity)
	url := issuanceUnderTest(t, logbook)

	issue(t, url)

	assembly := ldvtest.ByName(logbook.Written(), "dataverwerking.attestatie-samenstellen")
	if len(assembly) != 1 {
		t.Fatalf("expected one assembly record, got %+v", logbook.Written())
	}
	if got := assembly[0].Attributes[ldv.AttrNextLogbookID]; got != "https://logboek.belastingdienst.nl/data-processing-operations" {
		t.Errorf("%s = %v, want the source logbook's read-API URI", ldv.AttrNextLogbookID, got)
	}
	// The extraction happens before any bronhouder is involved, so it points
	// nowhere — and an absent pointer must not be written as an empty one.
	extraction := ldvtest.ByName(logbook.Written(), "dataverwerking.pid-bsn-extractie")
	if _, present := extraction[0].Attributes[ldv.AttrNextLogbookID]; present {
		t.Error("the extraction record should carry no next-logbook pointer")
	}
}

func TestParseNextLogbooks(t *testing.T) {
	mapping := parseNextLogbooks(" belastingdienst=https://a.test/data-processing-operations , rvig=https://b.test/data-processing-operations ,, malformed ,=x, y= ")
	if len(mapping) != 2 {
		t.Fatalf("mapping = %#v, want the two well-formed entries", mapping)
	}
	if mapping["belastingdienst"] != "https://a.test/data-processing-operations" || mapping["rvig"] != "https://b.test/data-processing-operations" {
		t.Errorf("mapping = %#v", mapping)
	}
}
