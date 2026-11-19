package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ldv-logboek/internal/httpapi"
	"ldv-logboek/internal/ldv"
	"ldv-logboek/internal/sqlite"
	"ldv-logboek/internal/usageindex"

	ldvclient "gbo-demo/ldv-client"
)

// One test per acceptance criterion of #689, against a fake upstream (the
// sidecar) and a fake consent usage index, with the real core, store and
// read API.

const (
	activity      = "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-levering-onder-toestemming/v1"
	traceID       = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan    = "00f067aa0ba902b7"
	subjectID     = "LP-citizen-of-the-source"
	readToken     = "read-token"
	logbookID     = "99999999900000000200/ldv-lezen-bd"
	testConsentID = "c-7f3a"
	sourceReply   = `{"data":{"ingeschrevenPersoon":{"bsn":"123456789"}}}`
)

type chain struct {
	proxy *httptest.Server
	read  *httptest.Server

	mu              sync.Mutex
	upstreamStatus  int
	upstreamHeaders []http.Header
	registrations   []map[string]string
	refuseIndex     bool
	repository      *sqlite.Repository
}

func newChain(t *testing.T) *chain {
	t.Helper()
	c := &chain{upstreamStatus: http.StatusOK}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.upstreamHeaders = append(c.upstreamHeaders, r.Header.Clone())
		status := c.upstreamStatus
		c.mu.Unlock()
		// The sidecar names the citizen for the component (D1).
		w.Header().Set(ldvclient.HeaderSubjectID, subjectID)
		w.Header().Set(ldvclient.HeaderSubjectIDType, ldvclient.SubjectTypePseudonym)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(sourceReply))
	}))
	t.Cleanup(upstream.Close)

	index := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.refuseIndex {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		var registration map[string]string
		_ = json.NewDecoder(r.Body).Decode(&registration)
		c.registrations = append(c.registrations, registration)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(index.Close)

	register, err := ldv.LoadRegister("../../config/verwerkingsactiviteiten-bd.json")
	if err != nil {
		t.Fatal(err)
	}
	c.repository, err = sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.repository.Close() })
	logbook, err := ldv.NewLogbook(c.repository, register, nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := ldv.NewDeliveryRecorder(logbook,
		usageindex.Client{URL: index.URL, LogbookID: logbookID, HTTP: index.Client()}, activity)
	if err != nil {
		t.Fatal(err)
	}
	c.proxy = httptest.NewServer(&Handler{Upstream: upstream.URL, Client: upstream.Client(), Recorder: recorder})
	t.Cleanup(c.proxy.Close)
	c.read = httptest.NewServer(httpapi.NewHandler(logbook, "write-token", readToken, "https://logboek.belastingdienst.nl/ldv-lezen-bd"))
	t.Cleanup(c.read.Close)
	return c
}

func consentToken() string {
	encode := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(map[string]any{"consent_id": testConsentID})
	return encode([]byte(`{"alg":"ES256"}`)) + "." + encode(payload) + ".signature"
}

func (c *chain) post(t *testing.T, withToken bool) *http.Response {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, c.proxy.URL+"/graphql", strings.NewReader(`{"query":"{x}"}`))
	request.Header.Set("traceparent", "00-"+traceID+"-"+callerSpan+"-01")
	if withToken {
		request.Header.Set(ConsentTokenHeader, consentToken())
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// readBack reads the component logbook by trace-id through the read API.
func (c *chain) readBack(t *testing.T) []map[string]any {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, c.read.URL+"/data-processing-operations",
		bytes.NewBufferString(`{"traceId":"`+traceID+`"}`))
	request.Header.Set("Authorization", "Bearer "+readToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var out struct {
		Operations []map[string]any `json:"dataProcessingOperations"`
	}
	if err := json.NewDecoder(response.Body).Decode(&out); err != nil {
		t.Fatalf("read back: status %d: %v", response.StatusCode, err)
	}
	return out.Operations
}

func (c *chain) records(t *testing.T) int {
	t.Helper()
	count, err := c.repository.Count(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return count
}

// A consent token and a 200: one record with the request's trace-id, read
// back by trace-id; the trace is registered once; the data goes through
// without the sidecar's subject header.
func TestADeliveryUnderAConsentIsRecordedRegisteredAndReadable(t *testing.T) {
	c := newChain(t)
	response := c.post(t, true)
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != sourceReply {
		t.Fatalf("status %d, body %s", response.StatusCode, body)
	}
	if response.Header.Get(ldvclient.HeaderSubjectID) != "" || response.Header.Get(ldvclient.HeaderSubjectIDType) != "" {
		t.Error("the sidecar's subject header reached the caller")
	}

	operations := c.readBack(t)
	if len(operations) != 1 {
		t.Fatalf("records under the trace = %d, want 1", len(operations))
	}
	if got := strings.ReplaceAll(operations[0]["traceId"].(string), "-", ""); got != traceID {
		t.Errorf("traceId = %v", operations[0]["traceId"])
	}

	if len(c.registrations) != 1 {
		t.Fatalf("registrations = %v", c.registrations)
	}
	registration := c.registrations[0]
	if registration["logbook_id"] != logbookID || registration["trace_id"] != traceID || registration["grondslag_ref"] != testConsentID {
		t.Errorf("registration = %v", registration)
	}
}

// The record names the citizen as the sidecar named them, not the consent.
func TestTheRecordNamesTheCitizen(t *testing.T) {
	c := newChain(t)
	c.post(t, true)
	attributes := c.readBack(t)[0]["attributes"].(map[string]any)
	core := attributes["dpl"].(map[string]any)["core"].(map[string]any)
	if core["dataSubjectId"] != subjectID {
		t.Errorf("dataSubjectId = %v", core["dataSubjectId"])
	}
	encoded, _ := json.Marshal(attributes)
	if strings.Contains(string(encoded), testConsentID) {
		t.Errorf("the record carries the consent: %s", encoded)
	}
}

// The source's own records stay in the same trace: the sidecar gets a
// traceparent with the request's trace-id and the component's span as
// parent.
func TestTheSourceStaysInTheSameTrace(t *testing.T) {
	c := newChain(t)
	c.post(t, true)
	forwarded := c.upstreamHeaders[0]
	if got := ldvclient.TraceID(t.Context(), forwarded); got != traceID {
		t.Errorf("forwarded trace = %s", got)
	}
	parent := ldvclient.ParentSpanFor(forwarded, traceID)
	record := c.readBack(t)[0]
	if parent == "" || parent == callerSpan || record["spanId"] != parent || record["parentSpanId"] != callerSpan {
		t.Errorf("record span %v parent %v, the sidecar's parent %s", record["spanId"], record["parentSpanId"], parent)
	}
}

// Another status: no record, the response passes unchanged.
func TestAnotherStatusPassesUnrecorded(t *testing.T) {
	c := newChain(t)
	c.upstreamStatus = http.StatusBadRequest
	response := c.post(t, true)
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadRequest || string(body) != sourceReply {
		t.Fatalf("status %d, body %s", response.StatusCode, body)
	}
	if c.records(t) != 0 || len(c.registrations) != 0 {
		t.Error("a non-200 was recorded")
	}
}

// No consent token: forwarded, no record.
func TestARequestWithoutAConsentTokenIsForwardedUnrecorded(t *testing.T) {
	c := newChain(t)
	if response := c.post(t, false); response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	if c.records(t) != 0 || len(c.registrations) != 0 {
		t.Error("a request without a consent token was recorded")
	}
}

// The record cannot be stored: an error, not the data.
func TestARecordThatCannotBeStoredWithholdsTheData(t *testing.T) {
	c := newChain(t)
	_ = c.repository.Close()
	response := c.post(t, true)
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadGateway || len(body) != 0 {
		t.Fatalf("status %d, body %q", response.StatusCode, body)
	}
}

// The trace cannot be registered: an error, not the data.
func TestAFailedRegistrationWithholdsTheData(t *testing.T) {
	c := newChain(t)
	c.refuseIndex = true
	response := c.post(t, true)
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadGateway || len(body) != 0 {
		t.Fatalf("status %d, body %q", response.StatusCode, body)
	}
}

// A delivery withheld because its registration failed leaves no record, also
// in a trace an earlier delivery already registered. The register counts
// every record under a registered trace, so such a record would show as a
// use of a delivery the consumer never got (review of #706/#708).
func TestAWithheldDeliveryInARegisteredTraceLeavesNoRecord(t *testing.T) {
	c := newChain(t)
	if response := c.post(t, true); response.StatusCode != http.StatusOK {
		t.Fatalf("first delivery: status %d", response.StatusCode)
	}
	c.refuseIndex = true
	if response := c.post(t, true); response.StatusCode != http.StatusBadGateway {
		t.Fatalf("second delivery: status %d, want 502", response.StatusCode)
	}
	if got := len(c.readBack(t)); got != 1 {
		t.Fatalf("records under the trace = %d, want the 1 delivery that went out", got)
	}
}
