package main

import (
	"encoding/base64"
	"encoding/json"
	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	resolutionActivity = "bd-pi-bsn-resolutie@v1"
	forwardActivity    = "bd-bronquery-doorgifte@v1"
	// The BSN bsnk-mock resolves the demo PI to. It must never appear in a
	// record, in either flow.
	demoBSN = "123456789"
	// Where BSNk's side of the resolution can be looked up.
	bsnkContact = "https://example.test/bsnk-contact"
)

// pseudonymToken is an Fsc-Authorization token carrying the countersigned
// grant property that selects the pseudonym flow.
func pseudonymToken(t *testing.T) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"prp": map[string]any{"subject_id_type": "pseudonym"},
		"sub": "AAAABBBBCCCCDDDDEEEE",
	})
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	return "Bearer header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// sidecarUnderTest wires the sidecar against stub upstream and BSNk servers
// plus a fake logbook, and returns the sidecar's own URL.
func sidecarUnderTest(t *testing.T, logbook *ldvtest.Logbook) string {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"bsn":"` + demoBSN + `"}}}`))
	}))
	t.Cleanup(upstream.Close)

	bsnk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"bsn":"` + demoBSN + `"}`))
	}))
	t.Cleanup(bsnk.Close)

	cfg := config{
		UpstreamURL:           upstream.URL,
		BSNkURL:               bsnk.URL,
		OwnPeerOIN:            "99999999900000000200",
		PseudonymVars:         "bsn",
		LDVResolutionActivity: resolutionActivity,
		LDVForwardActivity:    forwardActivity,
		LDVBSNkNextLogbookID:  bsnkContact,
	}
	client := logbook.Client(t, "bron-sidecar")
	sidecar := httptest.NewServer(newMux(cfg, &http.Client{Timeout: 5 * time.Second}, client))
	t.Cleanup(sidecar.Close)
	return sidecar.URL
}

func postQuery(t *testing.T, url string, headers map[string]string, body string) *http.Response {
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

// The pseudonym flow performs two Dataverwerkingen — resolving the PI and
// forwarding the query — and both must show up, nested, in the logboek.
func TestPseudonymFlowLogsBothDataverwerkingen(t *testing.T) {
	logbook := ldvtest.New(t, resolutionActivity, forwardActivity)
	url := sidecarUnderTest(t, logbook)

	response := postQuery(t, url, map[string]string{
		"Fsc-Authorization":  pseudonymToken(t),
		"Fsc-Transaction-Id": "0af76519-16cd-43dd-8448-eb211c80319c",
		"X-GBO-Scope":        "bd:ib:2025",
	}, `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"PI-abc123"}}`)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}

	records := logbook.Written()
	if len(records) != 2 {
		t.Fatalf("wrote %d records, want 2: %+v", len(records), records)
	}
	resolution := ldvtest.ByName(records, "dataverwerking.pi-bsn-resolutie")
	forward := ldvtest.ByName(records, "dataverwerking.bronquery-doorgifte")
	if len(resolution) != 1 || len(forward) != 1 {
		t.Fatalf("expected one record of each kind, got %+v", records)
	}

	// The hop carried no traceparent, so the Fsc-Transaction-Id is the trace
	// id, hyphens stripped — the same value the ADL and the FSC txlog carry.
	const wantTrace = "0af7651916cd43dd8448eb211c80319c"
	for _, record := range records {
		if record.TraceID != wantTrace {
			t.Errorf("record %q trace_id = %q, want the Fsc-Transaction-Id %q", record.Name, record.TraceID, wantTrace)
		}
	}
	if resolution[0].ParentSpanID != forward[0].SpanID {
		t.Errorf("the resolution record should hang under the forward record")
	}

	// The record about de-pseudonymisation names the Betrokkene by the PI the
	// request arrived with, never by the BSN it produced.
	if got := resolution[0].Attributes[ldv.AttrDataSubjectID]; got != "PI-abc123" {
		t.Errorf("resolution data_subject_id = %v, want PI-abc123", got)
	}
	if got := resolution[0].Attributes[ldv.AttrDataSubjectIDType]; got != ldv.SubjectTypePI {
		t.Errorf("resolution data_subject_id_type = %v, want %s", got, ldv.SubjectTypePI)
	}
	if got := forward[0].Attributes[ldv.AttrDataSubjectID]; got != "PI-abc123" {
		t.Errorf("forward data_subject_id = %v, want PI-abc123", got)
	}
	if got := resolution[0].Attributes[ldv.AttrProcessingActivityID]; got != resolutionActivity {
		t.Errorf("resolution processing_activity_id = %v, want %s", got, resolutionActivity)
	}
	if got := forward[0].Attributes[ldv.AttrProcessingActivityID]; got != forwardActivity {
		t.Errorf("forward processing_activity_id = %v, want %s", got, forwardActivity)
	}
	// The resolution called BSNk, another party, so it points there. The
	// forward called the source behind the sidecar, which belongs to the same
	// Verantwoordelijke and logs into the same logbook, so it points nowhere.
	if got := resolution[0].Attributes[ldv.AttrNextLogbookID]; got != bsnkContact {
		t.Errorf("resolution nextLogbookId = %v, want %s", got, bsnkContact)
	}
	if _, present := forward[0].Attributes[ldv.AttrNextLogbookID]; present {
		t.Errorf("the forward points onwards, but its source logs into the same logbook")
	}
	// The request was initiated by another application, on the far side of an
	// FSC boundary.
	// §3.2.2.9 defines the processor as a URL, so an FSC peer id gets one.
	if got, want := forward[0].Attributes[ldv.AttrForeignOperationProcessor], ldv.DefaultPeerURIBase+"/AAAABBBBCCCCDDDDEEEE"; got != want {
		t.Errorf("foreign_operation.processor = %v, want %v", got, want)
	}
	ldvtest.AssertNoBSN(t, records, demoBSN)
}

// A caller that sends traceparent is followed, not overruled: its trace id is
// taken over unchanged and its span becomes the forward's parent (§3.3.1). The
// Fsc-Transaction-Id stays FSC's own; the ADL links the two.
func TestACallersTraceparentIsTakenOver(t *testing.T) {
	const callersTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	const callersSpan = "00f067aa0ba902b7"

	logbook := ldvtest.New(t, resolutionActivity, forwardActivity)
	url := sidecarUnderTest(t, logbook)

	response := postQuery(t, url, map[string]string{
		"Fsc-Authorization":  pseudonymToken(t),
		"Fsc-Transaction-Id": "0af76519-16cd-43dd-8448-eb211c80319c",
		"traceparent":        "00-" + callersTrace + "-" + callersSpan + "-01",
		"X-GBO-Scope":        "bd:ib:2025",
	}, `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"PI-abc123"}}`)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}

	records := logbook.Written()
	for _, record := range records {
		if record.TraceID != callersTrace {
			t.Errorf("record %q trace_id = %q, want the caller's trace %q", record.Name, record.TraceID, callersTrace)
		}
	}
	forward := ldvtest.ByName(records, "dataverwerking.bronquery-doorgifte")
	if len(forward) != 1 {
		t.Fatalf("expected one forward record, got %+v", records)
	}
	if forward[0].ParentSpanID != callersSpan {
		t.Errorf("forward parent_span_id = %q, want the caller's span %q", forward[0].ParentSpanID, callersSpan)
	}
}

// The direct (EUDI) flow holds only a BSN. It still logs the forward, and
// still may not name the Betrokkene by that BSN.
func TestDirectFlowLogsTheForwardUnderALocalPseudonym(t *testing.T) {
	logbook := ldvtest.New(t, resolutionActivity, forwardActivity)
	url := sidecarUnderTest(t, logbook)

	response := postQuery(t, url, nil,
		`{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"`+demoBSN+`"}}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	records := logbook.Written()
	if len(records) != 1 {
		t.Fatalf("wrote %d records, want 1 (no de-pseudonymisation happens): %+v", len(records), records)
	}
	if got := records[0].Attributes[ldv.AttrDataSubjectIDType]; got != ldv.SubjectTypePseudonym {
		t.Errorf("data_subject_id_type = %v, want %s", got, ldv.SubjectTypePseudonym)
	}
	subject, _ := records[0].Attributes[ldv.AttrDataSubjectID].(string)
	if !strings.HasPrefix(subject, "LP-") {
		t.Errorf("data_subject_id = %q, want a logbook-local pseudonym", subject)
	}
	ldvtest.AssertNoBSN(t, records, demoBSN)
}

// Fail-closed: a processing that cannot be logged does not deliver. This is
// the property that separates an LDV record from an observability span, so it
// is asserted rather than assumed.
func TestAForwardThatCannotBeLoggedWithholdsTheResponse(t *testing.T) {
	logbook := ldvtest.New(t, resolutionActivity, forwardActivity)
	url := sidecarUnderTest(t, logbook)
	logbook.RefuseEverything()

	response := postQuery(t, url, nil,
		`{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"`+demoBSN+`"}}`)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), demoBSN) {
		t.Fatalf("the source response leaked despite the refused record: %s", body)
	}
}

// The source has to be able to file its records under the same trace and
// below the sidecar's, and to name the Betrokkene the same way.
func TestTheSidecarPassesTraceMetadataToTheSource(t *testing.T) {
	logbook := ldvtest.New(t, resolutionActivity, forwardActivity)

	var received http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer upstream.Close()
	bsnk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"bsn":"` + demoBSN + `"}`))
	}))
	defer bsnk.Close()

	cfg := config{
		UpstreamURL:           upstream.URL,
		BSNkURL:               bsnk.URL,
		PseudonymVars:         "bsn",
		LDVResolutionActivity: resolutionActivity,
		LDVForwardActivity:    forwardActivity,
	}
	sidecar := httptest.NewServer(newMux(cfg, &http.Client{Timeout: 5 * time.Second}, logbook.Client(t, "bron-sidecar")))
	defer sidecar.Close()

	postQuery(t, sidecar.URL, map[string]string{"Fsc-Authorization": pseudonymToken(t)},
		`{"query":"q","variables":{"bsn":"PI-abc123"}}`)

	// §3.1: the trace crosses on the standard traceparent, not on a header
	// of our own.
	traceContext := ldv.TraceContextFrom(t.Context(), received, "")
	if !ldv.IsTraceID(traceContext.TraceID) {
		t.Fatalf("the source was given no usable traceparent: %q", received.Get("traceparent"))
	}
	if got := ldv.ParentSpanFor(received, traceContext.TraceID); !ldv.IsSpanID(got) {
		t.Errorf("parent span = %q, want the sidecar's forward span", got)
	}
	if got := received.Get(ldv.HeaderSubjectID); got != "PI-abc123" {
		t.Errorf("subject header = %q, want the PI so both components name the Betrokkene alike", got)
	}
	if got := received.Get(ldv.HeaderSubjectIDType); got != ldv.SubjectTypePI {
		t.Errorf("subject type header = %q, want %s", got, ldv.SubjectTypePI)
	}
}

// A request that identifies no Betrokkene processes no personal data, so
// there is nothing for the logbook to record.
func TestARequestWithoutASubjectWritesNoRecord(t *testing.T) {
	logbook := ldvtest.New(t, resolutionActivity, forwardActivity)
	url := sidecarUnderTest(t, logbook)

	response := postQuery(t, url, nil, `{"query":"{__schema{types{name}}}"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("wrote %d records for a subject-less query: %+v", len(records), records)
	}
}
