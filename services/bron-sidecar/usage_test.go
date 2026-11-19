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

const usageLogbookID = ownOIN + "/bd"

// fakeIndex stands in for the consent usage index behind the Outway.
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
		if r.URL.Path != "/consent-usage-index/registrations" {
			http.Error(w, "unknown path "+r.URL.Path, http.StatusNotFound)
			return
		}
		var registration map[string]string
		_ = json.NewDecoder(r.Body).Decode(&registration)
		index.registrations = append(index.registrations, registration)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)
	index.url = server.URL + "/consent-usage-index"
	return index
}

func (f *fakeIndex) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.registrations)
}

func withIndex(index *fakeIndex) func(*config) {
	return func(cfg *config) {
		cfg.UsageIndexURL = index.url
		cfg.UsageLogbookID = usageLogbookID
	}
}

func consentHeaders(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"Fsc-Authorization":   callerToken(t),
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}
}

// A 200 under a consent leaves one registration, under the trace of the
// forward record and the consent of the token.
func TestADeliveryUnderAConsentIsRegisteredOnce(t *testing.T) {
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	index := newFakeIndex(t)
	c := sidecarUnderTest(t, logbook, withIndex(index))

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

	var forwardTrace string
	for _, record := range logbook.Written() {
		if record.Name == "dataverwerking.bronquery-doorgifte" {
			forwardTrace = record.TraceID
		}
	}
	if forwardTrace == "" || registration["trace_id"] != forwardTrace {
		t.Errorf("registered trace %q, want the forward record's %q", registration["trace_id"], forwardTrace)
	}
}

// Without a consent token nothing was delivered under a consent.
func TestARequestWithoutAConsentTokenIsNotRegistered(t *testing.T) {
	index := newFakeIndex(t)
	c := sidecarUnderTest(t, ldvtest.New(t, decryptionActivity, forwardActivity), withIndex(index))

	response := postQuery(t, c.url, nil,
		`{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"`+demoBSN+`"}}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if index.count() != 0 {
		t.Fatalf("registrations = %d, want 0", index.count())
	}
}

// A source that answers anything but 200 delivered nothing.
func TestAnotherStatusIsNotRegistered(t *testing.T) {
	index := newFakeIndex(t)
	c := sidecarUnderTest(t, ldvtest.New(t, decryptionActivity, forwardActivity), withIndex(index))
	c.upstreamStatus = http.StatusBadGateway

	response := postQuery(t, c.url, consentHeaders(t), consentQuery)
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want the source's 502", response.StatusCode)
	}
	if index.count() != 0 {
		t.Fatalf("registrations = %d, want 0", index.count())
	}
}

// Fail-closed: a delivery the index does not confirm does not reach the
// consumer.
func TestAFailedRegistrationWithholdsTheData(t *testing.T) {
	index := newFakeIndex(t)
	index.refuse = true
	c := sidecarUnderTest(t, ldvtest.New(t, decryptionActivity, forwardActivity), withIndex(index))

	response := postQuery(t, c.url, consentHeaders(t), consentQuery)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), demoBSN) {
		t.Fatalf("the source response leaked despite the failed registration: %s", body)
	}
}

// Without the setting the sidecar registers nothing and still delivers.
func TestWithoutTheSettingNothingIsRegistered(t *testing.T) {
	c := sidecarUnderTest(t, ldvtest.New(t, decryptionActivity, forwardActivity))
	if response := postQuery(t, c.url, consentHeaders(t), consentQuery); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestHalfASettingIsRefusedAtStart(t *testing.T) {
	cfg := config{UsageIndexURL: "http://bd-outway:8080/consent-usage-index"}
	if _, err := newRegistrar(cfg, http.DefaultClient); err == nil {
		t.Fatal("a URL without a logbook ID was accepted")
	}
}
