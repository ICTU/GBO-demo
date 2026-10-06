package main

import (
	"encoding/base64"
	"encoding/json"
	"gbo-demo/bron-sidecar/substitution"
	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	decryptionActivity = "bd-identiteit-ontsleuteling@v1"
	forwardActivity    = "bd-bronquery-doorgifte@v1"
	// The BSN the encrypted identity in the consent token decrypts to. It
	// must never appear in a record, in either flow.
	demoBSN = "123456789"
	ownOIN  = "99999999900000000200"
	// A key file of this source, as the key request hands it over.
	keyFile = "-----BEGIN BSNK MOCK DV KEY-----\nRecipient: " + ownOIN + "\nType: EI Decryption\n\nbm8ga2V5IG1hdGVyaWFs\n-----END BSNK MOCK DV KEY-----\n"
	// The query a consent-based consumer sends: the subject is the placeholder.
	consentQuery = `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"` + substitution.IdentityPlaceholder + `"}}`
	// The same query to an API that takes the source's own pseudonym.
	pseudonymQuery = `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"` + substitution.PseudonymPlaceholder + `"}}`
	// sourcePseudonym is the source's own pseudonym of the citizen, as its
	// decryption component reads it from the pseudonym made for it.
	sourcePseudonym = "eyJyZWNpcGllbnQiOiJzb3VyY2UifQ"
)

// callerToken is the Fsc-Authorization token of the consumer, as the Inway
// passes it on. The sidecar reads only who called from it.
func callerToken(t *testing.T) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"sub": "AAAABBBBCCCCDDDDEEEE"})
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	return "Bearer header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// consentToken carries an encrypted identity and an encrypted pseudonym for
// the given party. The sidecar does not verify the token: the PDP did, before
// the Inway proxied.
func consentToken(t *testing.T, party string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"consent_id": "c-7f3a",
		"encrypted_subject": map[string]any{
			party: map[string]any{
				"identity":  map[string]any{"key_set_version": 20260101, "value": "identity-for:" + party},
				"pseudonym": map[string]any{"key_set_version": 20260101, "value": "pseudonym-for:" + party},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"ES256","typ":"gbo-consent+jwt"}`)) + "." + encode(payload) + ".signature"
}

// chain is a sidecar under test with what stands around it: the source
// behind it, the source's decryption component and a logbook.
type chain struct {
	url string

	mu sync.Mutex
	// upstreamBodies and upstreamHeaders are what the source received.
	upstreamBodies  []string
	upstreamHeaders []http.Header
	// decryptionRequests are what the decryption component was asked.
	decryptionRequests []map[string]any
	// refuseDecryption makes the component answer as it does for a value it
	// cannot read.
	refuseDecryption bool
}

// sidecarUnderTest wires the sidecar against a stub source and a stub
// decryption component plus a fake logbook. The component reads the test's
// own values: "identity-for:<oin>" is the demo BSN and "pseudonym-for:<oin>"
// the source's pseudonym, made for that OIN.
func sidecarUnderTest(t *testing.T, logbook *ldvtest.Logbook) *chain {
	t.Helper()
	c := &chain{}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.upstreamBodies = append(c.upstreamBodies, string(body))
		c.upstreamHeaders = append(c.upstreamHeaders, r.Header.Clone())
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"bsn":"` + demoBSN + `"}}}`))
	}))
	t.Cleanup(upstream.Close)

	component := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		c.mu.Lock()
		c.decryptionRequests = append(c.decryptionRequests, request)
		refuse := c.refuseDecryption
		c.mu.Unlock()
		if refuse {
			http.Error(w, "no matching key", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/signed-encrypted-identity":
			value, _ := request["signedEncryptedIdentity"].(string)
			recipient := strings.TrimPrefix(value, "identity-for:")
			_, _ = w.Write([]byte(`{"bsn":"` + demoBSN + `","decodedInput":{"signedEI":{"encryptedIdentity":{"recipient":"` + recipient + `"}}}}`))
		case "/signed-encrypted-pseudonym":
			value, _ := request["signedEncryptedPseudonym"].(string)
			recipient := strings.TrimPrefix(value, "pseudonym-for:")
			_, _ = w.Write([]byte(`{"pseudonym":"` + sourcePseudonym + `","decodedPseudonym":{"recipient":"` + recipient + `"}}`))
		default:
			http.Error(w, "unknown endpoint", http.StatusNotFound)
		}
	}))
	t.Cleanup(component.Close)

	keys := t.TempDir()
	for name, content := range map[string]string{
		"ei-decryption.pem": keyFile,
		"scheme-keys.json":  `{"urn:nl-gdi-eid:1.0:pp-key:mock:1:IP_P:1":"c2NoZW1lIGtleQ=="}`,
	} {
		if err := os.WriteFile(filepath.Join(keys, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	cfg := config{
		UpstreamURL:           upstream.URL,
		DecryptionURL:         component.URL,
		SubjectKeysDir:        keys,
		OwnPeerOIN:            ownOIN,
		PseudonymVars:         "bsn",
		LDVDecryptionActivity: decryptionActivity,
		LDVForwardActivity:    forwardActivity,
	}
	var client *ldv.Client
	if logbook != nil {
		client = logbook.Client(t, "bron-sidecar")
	}
	sidecar := httptest.NewServer(mustMux(t, cfg, &http.Client{Timeout: 5 * time.Second}, client))
	t.Cleanup(sidecar.Close)
	c.url = sidecar.URL
	return c
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

// Under a consent token the source gets the BSN in the placeholder's place,
// read from the value the token carries for this source, with this source's
// keys, by its own decryption component.
func TestUnderAConsentTokenTheSourceReceivesTheBSN(t *testing.T) {
	c := sidecarUnderTest(t, nil)

	response := postQuery(t, c.url, map[string]string{
		"Fsc-Authorization":   callerToken(t),
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}, consentQuery)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}

	if len(c.upstreamBodies) != 1 {
		t.Fatalf("the source was called %d times, want 1", len(c.upstreamBodies))
	}
	var forwarded struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(c.upstreamBodies[0]), &forwarded); err != nil {
		t.Fatalf("forwarded body: %v", err)
	}
	if forwarded.Variables["bsn"] != demoBSN {
		t.Errorf("the source received %v, want the BSN in the placeholder's place", forwarded.Variables)
	}

	if len(c.decryptionRequests) != 1 {
		t.Fatalf("the decryption component was called %d times, want 1", len(c.decryptionRequests))
	}
	request := c.decryptionRequests[0]
	if request["signedEncryptedIdentity"] != "identity-for:"+ownOIN {
		t.Errorf("decrypted %v, want the value made for this source", request["signedEncryptedIdentity"])
	}
	// The component keeps no keys: the source hands its own over with the value.
	if keys, _ := request["serviceProviderKeys"].([]any); len(keys) != 1 || keys[0] != keyFile {
		t.Errorf("serviceProviderKeys = %v, want this source's key file", request["serviceProviderKeys"])
	}
	if schemeKeys, _ := request["schemeKeys"].(map[string]any); len(schemeKeys) != 1 {
		t.Errorf("schemeKeys = %v, want the scheme keys from the key directory", request["schemeKeys"])
	}
}

// Under a consent token, a query to an API that takes the source's own
// pseudonym gets it in the placeholder's place, read from the pseudonym the
// token carries for this source. The identity is not read, and the records
// name the Betrokkene by a logbook-local pseudonym, not by the source's BSNk
// pseudonym itself.
func TestUnderAConsentTokenThePseudonymPlaceholderBecomesTheSourcesPseudonym(t *testing.T) {
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	c := sidecarUnderTest(t, logbook)

	response := postQuery(t, c.url, map[string]string{
		"Fsc-Authorization":   callerToken(t),
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}, pseudonymQuery)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}

	if len(c.upstreamBodies) != 1 {
		t.Fatalf("the source was called %d times, want 1", len(c.upstreamBodies))
	}
	var forwarded struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(c.upstreamBodies[0]), &forwarded); err != nil {
		t.Fatalf("forwarded body: %v", err)
	}
	if forwarded.Variables["bsn"] != sourcePseudonym {
		t.Errorf("the source received %v, want its pseudonym in the placeholder's place", forwarded.Variables)
	}
	if len(c.decryptionRequests) != 1 || c.decryptionRequests[0]["signedEncryptedPseudonym"] != "pseudonym-for:"+ownOIN {
		t.Errorf("decryption requests = %v, want this source's pseudonym alone", c.decryptionRequests)
	}

	records := logbook.Written()
	if len(records) != 2 {
		t.Fatalf("wrote %d records, want 2: %+v", len(records), records)
	}
	for _, record := range records {
		subject, _ := record.Attributes[ldv.AttrDataSubjectID].(string)
		if !strings.HasPrefix(subject, "LP-") {
			t.Errorf("record %q names the Betrokkene %q, want a logbook-local pseudonym", record.Name, subject)
		}
		if encoded, _ := json.Marshal(record); strings.Contains(string(encoded), sourcePseudonym) {
			t.Errorf("record %q contains the source's pseudonym itself: %s", record.Name, encoded)
		}
	}
	if records[0].Attributes[ldv.AttrDataSubjectID] != records[1].Attributes[ldv.AttrDataSubjectID] {
		t.Error("the decryption and the forward name the same Betrokkene differently")
	}
}

// A value made for another party's OIN is not accepted by the source, and
// the source is not called.
func TestAValueForAnotherPartyDoesNotReachTheSource(t *testing.T) {
	c := sidecarUnderTest(t, nil)

	response := postQuery(t, c.url, map[string]string{
		"X-GBO-Consent-Token": consentToken(t, "99999999900000000210"),
	}, consentQuery)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if len(c.upstreamBodies) != 0 || len(c.decryptionRequests) != 0 {
		t.Errorf("source calls %d, decryptions %d; want none of either", len(c.upstreamBodies), len(c.decryptionRequests))
	}
}

// Under a consent token a literal subject is refused here as well: the PDP
// denies it, so a request that carries one did not come through the PDP.
func TestALiteralSubjectUnderAConsentTokenDoesNotReachTheSource(t *testing.T) {
	c := sidecarUnderTest(t, nil)

	response := postQuery(t, c.url, map[string]string{
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}, `{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"`+demoBSN+`"}}`)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if len(c.upstreamBodies) != 0 || len(c.decryptionRequests) != 0 {
		t.Errorf("source calls %d, decryptions %d; want none of either", len(c.upstreamBodies), len(c.decryptionRequests))
	}
}

// The consent flow performs two Dataverwerkingen — reading the identity and
// forwarding the query — and both must show up, nested, in the logboek.
func TestConsentFlowLogsBothDataverwerkingen(t *testing.T) {
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	c := sidecarUnderTest(t, logbook)

	response := postQuery(t, c.url, map[string]string{
		"Fsc-Authorization":   callerToken(t),
		"Fsc-Transaction-Id":  "0af76519-16cd-43dd-8448-eb211c80319c",
		"X-GBO-Scope":         "bd:ib:2025",
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}, consentQuery)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}

	records := logbook.Written()
	if len(records) != 2 {
		t.Fatalf("wrote %d records, want 2: %+v", len(records), records)
	}
	decryption := ldvtest.ByName(records, "dataverwerking.identiteit-ontsleuteling")
	forward := ldvtest.ByName(records, "dataverwerking.bronquery-doorgifte")
	if len(decryption) != 1 || len(forward) != 1 {
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
	if decryption[0].ParentSpanID != forward[0].SpanID {
		t.Errorf("the decryption record should hang under the forward record")
	}

	// Both records name the Betrokkene by this source's own pseudonym: not the
	// BSN the decryption produced, and nothing the request arrived with.
	for _, record := range records {
		subject, _ := record.Attributes[ldv.AttrDataSubjectID].(string)
		if !strings.HasPrefix(subject, "LP-") || record.Attributes[ldv.AttrDataSubjectIDType] != ldv.SubjectTypePseudonym {
			t.Errorf("record %q names the Betrokkene %q (%v), want a logbook-local pseudonym",
				record.Name, subject, record.Attributes[ldv.AttrDataSubjectIDType])
		}
		if encoded, _ := json.Marshal(record); strings.Contains(string(encoded), "identity-for:") || strings.Contains(string(encoded), "c-7f3a") {
			t.Errorf("record %q contains the token's value or the consent id: %s", record.Name, encoded)
		}
	}
	if decryption[0].Attributes[ldv.AttrDataSubjectID] != forward[0].Attributes[ldv.AttrDataSubjectID] {
		t.Error("the decryption and the forward name the same Betrokkene differently")
	}
	if got := decryption[0].Attributes[ldv.AttrProcessingActivityID]; got != decryptionActivity {
		t.Errorf("decryption processing_activity_id = %v, want %s", got, decryptionActivity)
	}
	if got := forward[0].Attributes[ldv.AttrProcessingActivityID]; got != forwardActivity {
		t.Errorf("forward processing_activity_id = %v, want %s", got, forwardActivity)
	}
	// Neither record points onwards. The source read the value itself, with
	// its own keys, and the source behind the sidecar belongs to the same
	// Verantwoordelijke and logs into the same logbook.
	for _, record := range records {
		if _, present := record.Attributes[ldv.AttrNextLogbookID]; present {
			t.Errorf("record %q points to another logbook", record.Name)
		}
	}
	// The request was initiated by another application, on the far side of an
	// FSC boundary.
	// §3.2.2.9 defines the processor as a URL, so an FSC peer id gets one.
	if got, want := forward[0].Attributes[ldv.AttrForeignOperationProcessor], ldv.DefaultPeerURIBase+"/AAAABBBBCCCCDDDDEEEE"; got != want {
		t.Errorf("foreign_operation.processor = %v, want %v", got, want)
	}
	ldvtest.AssertNoBSN(t, records, demoBSN)
}

// A value that cannot be read is a Dataverwerking that failed. It is logged,
// named by a pseudonym of the consent, and the source is not called.
func TestAFailedDecryptionIsLoggedAndDoesNotReachTheSource(t *testing.T) {
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	c := sidecarUnderTest(t, logbook)
	c.refuseDecryption = true

	response := postQuery(t, c.url, map[string]string{
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}, consentQuery)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	if len(c.upstreamBodies) != 0 {
		t.Errorf("the source was called %d times after a failed decryption", len(c.upstreamBodies))
	}

	records := logbook.Written()
	if len(records) != 1 || records[0].Name != "dataverwerking.identiteit-ontsleuteling" {
		t.Fatalf("records = %+v, want the failed decryption alone", records)
	}
	if records[0].Status != ldv.StatusError {
		t.Errorf("status = %q, want an error", records[0].Status)
	}
	if subject, _ := records[0].Attributes[ldv.AttrDataSubjectID].(string); !strings.HasPrefix(subject, "LP-") {
		t.Errorf("data_subject_id = %q, want a logbook-local pseudonym", subject)
	}
}

// A caller that sends traceparent is followed, not overruled: its trace id is
// taken over unchanged and its span becomes the forward's parent (§3.3.1). The
// Fsc-Transaction-Id stays FSC's own; the ADL links the two.
func TestACallersTraceparentIsTakenOver(t *testing.T) {
	const callersTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	const callersSpan = "00f067aa0ba902b7"

	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	c := sidecarUnderTest(t, logbook)

	response := postQuery(t, c.url, map[string]string{
		"Fsc-Authorization":   callerToken(t),
		"Fsc-Transaction-Id":  "0af76519-16cd-43dd-8448-eb211c80319c",
		"traceparent":         "00-" + callersTrace + "-" + callersSpan + "-01",
		"X-GBO-Scope":         "bd:ib:2025",
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}, consentQuery)
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

// Without a consent token (the EUDI flow) the request holds a BSN. The
// sidecar still logs the forward, and still may not name the Betrokkene by
// that BSN.
func TestWithoutAConsentTokenTheForwardIsLoggedUnderALocalPseudonym(t *testing.T) {
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	c := sidecarUnderTest(t, logbook)

	response := postQuery(t, c.url, nil,
		`{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"`+demoBSN+`"}}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	records := logbook.Written()
	if len(records) != 1 {
		t.Fatalf("wrote %d records, want 1 (nothing is decrypted): %+v", len(records), records)
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
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	c := sidecarUnderTest(t, logbook)
	logbook.RefuseEverything()

	response := postQuery(t, c.url, nil,
		`{"query":"query($bsn: BSN!){ingeschrevenPersoon(bsn:$bsn){bsn}}","variables":{"bsn":"`+demoBSN+`"}}`)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), demoBSN) {
		t.Fatalf("the source response leaked despite the refused record: %s", body)
	}
}

// A decryption that cannot be logged goes no further: the source is not
// called with the BSN it produced.
func TestADecryptionThatCannotBeLoggedDoesNotReachTheSource(t *testing.T) {
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	c := sidecarUnderTest(t, logbook)
	logbook.RefuseEverything()

	response := postQuery(t, c.url, map[string]string{
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}, consentQuery)
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.StatusCode)
	}
	if len(c.upstreamBodies) != 0 {
		t.Errorf("the source was called %d times after an unlogged decryption", len(c.upstreamBodies))
	}
}

// The source has to be able to file its records under the same trace and
// below the sidecar's, and to name the Betrokkene the same way.
func TestTheSidecarPassesTraceMetadataToTheSource(t *testing.T) {
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	c := sidecarUnderTest(t, logbook)

	postQuery(t, c.url, map[string]string{
		"Fsc-Authorization":   callerToken(t),
		"X-GBO-Consent-Token": consentToken(t, ownOIN),
	}, consentQuery)
	if len(c.upstreamHeaders) != 1 {
		t.Fatalf("the source was called %d times, want 1", len(c.upstreamHeaders))
	}
	received := c.upstreamHeaders[0]

	// §3.1: the trace crosses on the standard traceparent, not on a header
	// of our own.
	traceContext := ldv.TraceContextFrom(t.Context(), received, "")
	if !ldv.IsTraceID(traceContext.TraceID) {
		t.Fatalf("the source was given no usable traceparent: %q", received.Get("traceparent"))
	}
	if got := ldv.ParentSpanFor(received, traceContext.TraceID); !ldv.IsSpanID(got) {
		t.Errorf("parent span = %q, want the sidecar's forward span", got)
	}
	// The sidecar's own pseudonym, so both components name the Betrokkene
	// alike.
	if got := received.Get(ldv.HeaderSubjectID); !strings.HasPrefix(got, "LP-") {
		t.Errorf("subject header = %q, want the sidecar's logbook-local pseudonym", got)
	}
	if got := received.Get(ldv.HeaderSubjectIDType); got != ldv.SubjectTypePseudonym {
		t.Errorf("subject type header = %q, want %s", got, ldv.SubjectTypePseudonym)
	}
}

// A request that identifies no Betrokkene processes no personal data, so
// there is nothing for the logbook to record.
func TestARequestWithoutASubjectWritesNoRecord(t *testing.T) {
	logbook := ldvtest.New(t, decryptionActivity, forwardActivity)
	c := sidecarUnderTest(t, logbook)

	response := postQuery(t, c.url, nil, `{"query":"{__schema{types{name}}}"}`)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if records := logbook.Written(); len(records) != 0 {
		t.Fatalf("wrote %d records for a subject-less query: %+v", len(records), records)
	}
}
