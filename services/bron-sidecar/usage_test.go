package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gbo-demo/ldv-client/ldvtest"
)

const (
	usageLogbookID   = ownOIN + "/ldv-lezen-bd"
	deliveryActivity = "bd-levering-onder-toestemming@v1"
)

// fakeIndex stands in for the data usage index behind the Outway.
type fakeIndex struct {
	url string

	mu            sync.Mutex
	registrations []map[string]string
	refuse        bool
}

func newFakeIndex(t *testing.T) *fakeIndex {
	t.Helper()
	index := &fakeIndex{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index.mu.Lock()
		defer index.mu.Unlock()
		if index.refuse {
			http.Error(w, "index unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/data-usage-index/registrations" {
			http.Error(w, "unknown path "+r.URL.Path, http.StatusNotFound)
			return
		}
		var registration map[string]string
		_ = json.NewDecoder(r.Body).Decode(&registration)
		index.registrations = append(index.registrations, registration)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	index.url = server.URL + "/data-usage-index"
	return index
}

func (f *fakeIndex) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registrations)
}

// withDeliveries makes the sidecar record deliveries: it registers at the
// index and writes its delivery records to a logbook of their own.
func withDeliveries(index *fakeIndex, deliveryLogbook *ldvtest.Logbook) func(*config) {
	return func(cfg *config) {
		cfg.UsageIndexURL = index.url
		cfg.UsageLogbookID = usageLogbookID
		cfg.DeliveryLogbookURL = deliveryLogbook.URL()
		cfg.DeliveryActivity = deliveryActivity
		cfg.LDVWriteToken = "test-token"
		cfg.LDVPseudonymKey = "test-key"
	}
}

func consentHeaders(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"Fsc-Authorization":   callerToken(t),
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}
}

const literalQuery = `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"` + demoBSN + `"}}`

// A 200 under a consent leaves one registration and one delivery record in
// the delivery logbook, under the trace of the forward record. The record
// names the citizen like the forward record does, and not the consent; the
// source's own logbook gets no delivery record.
func TestADeliveryUnderAConsentIsRegisteredAndRecorded(t *testing.T) {
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	deliveryLogbook := ldvtest.New(t, deliveryActivity)
	index := newFakeIndex(t)
	c := sidecarUnderTest(t, logbook, withDeliveries(index, deliveryLogbook))

	response := postQuery(t, c.url, consentHeaders(t), consentQuery)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	if index.count() != 1 {
		t.Fatalf("registrations = %d, want 1", index.count())
	}
	registration := index.registrations[0]
	if registration["logbook_id"] != usageLogbookID || registration["grondslag_type"] != "consent" || registration["grondslag_ref"] != "c-7f3a" {
		t.Errorf("registration = %v", registration)
	}

	var forwardTrace, forwardSpan, forwardSubject string
	for _, record := range logbook.Written() {
		if record.Name == deliveryRecordName {
			t.Error("the delivery record landed in the source's own logbook")
		}
		if record.Name == "dataverwerking.bronquery-doorgifte" {
			forwardTrace, forwardSpan = record.TraceID, record.SpanID
			forwardSubject, _ = record.Attributes["dpl.core.data_subject_id"].(string)
		}
	}
	deliveries := deliveryLogbook.Written()
	if len(deliveries) != 1 {
		t.Fatalf("delivery records = %d, want 1", len(deliveries))
	}
	delivery := deliveries[0]
	if delivery.Name != deliveryRecordName || delivery.TraceID != forwardTrace || delivery.ParentSpanID != forwardSpan || delivery.SpanID == forwardSpan {
		t.Errorf("delivery record = %+v, forward trace %s span %s", delivery, forwardTrace, forwardSpan)
	}
	if registration["trace_id"] != delivery.TraceID {
		t.Errorf("registered trace %q, the record has %q", registration["trace_id"], delivery.TraceID)
	}
	if subject, _ := delivery.Attributes["dpl.core.data_subject_id"].(string); subject == "" || subject != forwardSubject {
		t.Errorf("delivery names %q, the forward record %q", subject, forwardSubject)
	}
	encoded, _ := json.Marshal(delivery.Attributes)
	if strings.Contains(string(encoded), "c-7f3a") {
		t.Errorf("the delivery record carries the consent: %s", encoded)
	}
	ldvtest.AssertNoBSN(t, deliveries, demoBSN)
	if response.Header.Get("Gbo-Ldv-Subject-Id") != "" {
		t.Error("the subject pseudonym left the sidecar in a response header")
	}
}

// Without a consent token nothing was delivered under a consent.
func TestARequestWithoutAConsentTokenLeavesNoDelivery(t *testing.T) {
	deliveryLogbook := ldvtest.New(t, deliveryActivity)
	index := newFakeIndex(t)
	c := sidecarUnderTest(t, ldvtest.New(t, decryptionActivity, forwardActivity), withDeliveries(index, deliveryLogbook))

	if response := postQuery(t, c.url, nil, literalQuery); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if index.count() != 0 || len(deliveryLogbook.Written()) != 0 {
		t.Fatalf("registrations = %d, delivery records = %d, want none", index.count(), len(deliveryLogbook.Written()))
	}
}

// A source that answers anything but 200 delivered nothing.
func TestAnotherStatusLeavesNoDelivery(t *testing.T) {
	deliveryLogbook := ldvtest.New(t, deliveryActivity)
	index := newFakeIndex(t)
	c := sidecarUnderTest(t, ldvtest.New(t, decryptionActivity, forwardActivity), withDeliveries(index, deliveryLogbook))
	c.upstreamStatus = http.StatusBadGateway

	response := postQuery(t, c.url, consentHeaders(t), consentQuery)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want the source's 502", response.StatusCode)
	}
	if index.count() != 0 || len(deliveryLogbook.Written()) != 0 {
		t.Fatalf("registrations = %d, delivery records = %d, want none", index.count(), len(deliveryLogbook.Written()))
	}
}

// Fail-closed, registration first: a delivery the index does not confirm
// does not reach the consumer and leaves no record. A record left behind
// would count as a use once an earlier delivery had registered the trace.
func TestAFailedRegistrationWithholdsTheDataAndLeavesNoRecord(t *testing.T) {
	deliveryLogbook := ldvtest.New(t, deliveryActivity)
	index := newFakeIndex(t)
	index.refuse = true
	c := sidecarUnderTest(t, ldvtest.New(t, decryptionActivity, forwardActivity), withDeliveries(index, deliveryLogbook))

	response := postQuery(t, c.url, consentHeaders(t), consentQuery)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), demoBSN) {
		t.Fatalf("the source response leaked despite the failed registration: %s", body)
	}
	if len(deliveryLogbook.Written()) != 0 {
		t.Fatalf("a withheld delivery left %d records", len(deliveryLogbook.Written()))
	}
}

// Fail-closed on the record too: a delivery whose record the delivery
// logbook does not confirm is withheld.
func TestADeliveryRecordThatCannotBeWrittenWithholdsTheData(t *testing.T) {
	deliveryLogbook := ldvtest.New(t, deliveryActivity)
	deliveryLogbook.RefuseEverything()
	c := sidecarUnderTest(t, ldvtest.New(t, decryptionActivity, forwardActivity), withDeliveries(newFakeIndex(t), deliveryLogbook))

	response := postQuery(t, c.url, consentHeaders(t), consentQuery)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), demoBSN) {
		t.Fatalf("the source response leaked despite the unwritten record: %s", body)
	}
}

// Without the settings the sidecar records no deliveries and still delivers.
func TestWithoutTheSettingsNoDeliveryIsRecorded(t *testing.T) {
	c := sidecarUnderTest(t, ldvtest.New(t, decryptionActivity, forwardActivity))
	if response := postQuery(t, c.url, consentHeaders(t), consentQuery); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestHalfTheSettingsAreRefusedAtStart(t *testing.T) {
	cfg := config{UsageIndexURL: "http://bd-outway:8080/data-usage-index", UsageLogbookID: usageLogbookID}
	if _, err := newDeliveries(cfg, http.DefaultClient); err == nil {
		t.Fatal("an index without a delivery logbook was accepted")
	}
}
