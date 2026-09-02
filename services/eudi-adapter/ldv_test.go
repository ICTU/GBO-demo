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

// issuanceUnderTest wires the adapter against a stub metadata publisher and a
// stub Outway, with a logbook attached, and returns the adapter's URL.
func issuanceUnderTest(t *testing.T, logbook *ldvtest.Logbook) string {
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

	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data": {"ingeschrevenPersoon": {"heeftBelastingjaarAangifte": [{
			"belastingjaar": 2025,
			"status": "Definitief vastgesteld",
			"indieningsdatum": "2026-04-01",
			"verzamelinkomen": {"waarde": 43000.0, "valuta": "EUR"},
			"box1Inkomen": {"waarde": 41000.0, "valuta": "EUR"},
			"box2Inkomen": {"waarde": 1000.0, "valuta": "EUR"},
			"box3Inkomen": {"waarde": 1000.0, "valuta": "EUR"}
		}]}}}`))
	}))
	t.Cleanup(outway.Close)

	cfg := config{
		Port: "0", OutwayURL: outway.URL, SourceDataTransport: sourceTransportFSC,
		SourceDataFSCServiceReference: "bri", SourceDataFSCGrantHash: "data-grant",
	}
	client := newIssuanceLogbook(logbook.Client(t, "eudi-adapter"), map[string]string{"belastingdienst": "logboek-bd"})
	server := httptest.NewServer(testMux(cfg, http.DefaultClient, metadata, client))
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
	if got := assembly[0].Attributes["gbo.attestatie.claims"]; got == nil || got == float64(0) {
		t.Errorf("gbo.attestatie.claims = %v, want the number of claims", got)
	}
	if got := assembly[0].Attributes["gbo.source_oin"]; got != "99999999900000000200" {
		t.Errorf("gbo.source_oin = %v", got)
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

// The trace id is the Fsc-Transaction-Id the adapter mints, so the adapter's
// records and the source's records — which live in a different
// Verantwoordelijke's logbook — can be read together.
func TestIssuanceRecordsCarryTheFscTransactionID(t *testing.T) {
	logbook := ldvtest.New(t, allGBOActivities()...)
	url := issuanceUnderTest(t, logbook)

	issue(t, url)

	for _, record := range logbook.Written() {
		if ldv.NormalizeTraceID(record.TraceID) == "" {
			t.Errorf("record %q trace_id = %q, not an OTel trace id", record.Name, record.TraceID)
		}
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

	client := newIssuanceLogbook(logbook.Client(t, "eudi-adapter"), map[string]string{"belastingdienst": "logboek-bd"})
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

// normalizeTraceID is what lets one UUID serve as the trace id of three
// standards. Its edge cases decide whether records correlate at all.
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
	if got := assembly[0].Attributes[ldv.AttrNextLogbookID]; got != "logboek-bd" {
		t.Errorf("%s = %v, want logboek-bd", ldv.AttrNextLogbookID, got)
	}
	// The extraction happens before any bronhouder is involved, so it points
	// nowhere — and an absent pointer must not be written as an empty one.
	extraction := ldvtest.ByName(logbook.Written(), "dataverwerking.pid-bsn-extractie")
	if _, present := extraction[0].Attributes[ldv.AttrNextLogbookID]; present {
		t.Error("the extraction record should carry no next-logbook pointer")
	}
}

func TestParseNextLogbooks(t *testing.T) {
	mapping := parseNextLogbooks(" belastingdienst=logboek-bd , rvig=logboek-brp ,, malformed ,=x, y= ")
	if len(mapping) != 2 {
		t.Fatalf("mapping = %#v, want the two well-formed entries", mapping)
	}
	if mapping["belastingdienst"] != "logboek-bd" || mapping["rvig"] != "logboek-brp" {
		t.Errorf("mapping = %#v", mapping)
	}
}
