package ldvclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testConfig(url string) Config {
	return Config{
		ServiceName:  "test-service",
		LogbookURL:   url,
		WriteToken:   "test-token",
		PseudonymKey: "test-key",
	}
}

// No logbook URL means the component is not in an LDV chain at all. That is
// how the deliberately unsecured demo source stays out of it, so it is a
// documented behaviour rather than a convenience.
func TestNoLogbookURLMeansNoClient(t *testing.T) {
	client, err := New(Config{ServiceName: "test-service"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client != nil {
		t.Fatal("a component without a logbook URL must have no client")
	}
}

// A half-configured logbook would log nothing while looking configured, which
// is the one outcome the fail-closed design must not allow.
func TestALogbookURLWithoutATokenIsAnError(t *testing.T) {
	if _, err := New(Config{LogbookURL: "http://logboek:4016"}); err == nil {
		t.Fatal("a logbook URL without a write token must be a configuration error")
	}
}

// A pseudonym key is optional: components that never hold a BSN have nothing
// to derive one from.
func TestAClientWithoutAPseudonymKeyIsValid(t *testing.T) {
	client, err := New(Config{
		ServiceName: "consent-register", LogbookURL: "http://logboek:4016", WriteToken: "t",
	})
	if err != nil || client == nil {
		t.Fatalf("client = %v, err = %v", client, err)
	}
}

func TestWriteSendsTheRecordAndTheToken(t *testing.T) {
	var received Record
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := client.Write(context.Background(), Record{Name: "dataverwerking.test"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if authorization != "Bearer test-token" {
		t.Errorf("Authorization = %q", authorization)
	}
	// The resource says which component performed the processing; the caller
	// never has to remember to set it.
	if received.Resource["service.name"] != "test-service" {
		t.Errorf("resource = %#v", received.Resource)
	}
}

// A refused record must surface as an error so the caller can fail its own
// request. Anything else turns LDV back into best-effort logging.
func TestWriteReportsARefusedRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_record"}`, http.StatusUnprocessableEntity)
	}))
	defer server.Close()

	client, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	err = client.Write(context.Background(), Record{Name: "dataverwerking.test"})
	if err == nil {
		t.Fatal("a refused record must be reported")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("the error should carry the status, got %v", err)
	}
}

// One UUID serving as the trace id of three standards is the whole
// correlation story, so its edge cases matter.
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
		if got := NormalizeTraceID(input); got != want {
			t.Errorf("NormalizeTraceID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTraceIDPrefersTheChainsOwnHeaders(t *testing.T) {
	header := http.Header{}
	header.Set("Fsc-Transaction-Id", "0af76519-16cd-43dd-8448-eb211c80319c")
	if got := TraceID(context.Background(), header); got != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("TraceID = %q, want the Fsc-Transaction-Id", got)
	}
	// An LDV header set by an upstream component of the same Verantwoordelijke
	// wins, so both halves of one request file under one id.
	header.Set(HeaderTraceID, "11111111111111111111111111111111")
	if got := TraceID(context.Background(), header); got != "11111111111111111111111111111111" {
		t.Errorf("TraceID = %q, want the LDV header", got)
	}
	// A record is never dropped for want of a correlation handle.
	if got := TraceID(context.Background(), http.Header{}); NormalizeTraceID(got) == "" {
		t.Errorf("TraceID fallback = %q, not a trace id", got)
	}
}

func TestSpanIDsAreDistinct(t *testing.T) {
	first, second := SpanID(), SpanID()
	if first == second || len(first) != 16 {
		t.Fatalf("span ids = %q, %q", first, second)
	}
}

func TestAttributesDropEmptyValues(t *testing.T) {
	attributes := Attributes("test-overig@v1", "LP-abc", SubjectTypePseudonym, "", map[string]any{
		"gbo.present": "yes",
		"gbo.empty":   "",
		"gbo.nil":     nil,
		"gbo.number":  2025,
	})
	if _, present := attributes[AttrForeignOperationProcessor]; present {
		t.Error("an absent foreign processor must not be written as an empty attribute")
	}
	for _, dropped := range []string{"gbo.empty", "gbo.nil"} {
		if _, present := attributes[dropped]; present {
			t.Errorf("%s says nothing and should be dropped", dropped)
		}
	}
	if attributes["gbo.present"] != "yes" || attributes["gbo.number"] != 2025 {
		t.Errorf("set attributes must survive: %#v", attributes)
	}
}

func TestStatusReflectsTheOutcome(t *testing.T) {
	if Status(nil) != StatusOK || Status(http.ErrServerClosed) != StatusError {
		t.Error("Status does not follow the error")
	}
	if StatusFromHTTP(200) != StatusOK || StatusFromHTTP(404) != StatusError || StatusFromHTTP(500) != StatusError {
		t.Error("StatusFromHTTP does not follow the status code")
	}
}

func TestSubjectPrefersThePseudonymPassedByAnUpstreamComponent(t *testing.T) {
	client, err := New(testConfig("http://logboek:4016"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	header := http.Header{}
	header.Set(HeaderSubjectID, "PI-abc123")
	header.Set(HeaderSubjectIDType, SubjectTypePI)
	if id, idType := client.Subject(header, "123456789"); id != "PI-abc123" || idType != SubjectTypePI {
		t.Errorf("Subject = (%q, %q), want the PI the upstream passed on", id, idType)
	}
}

// An unrecognised type on the wire is treated as no type at all: the logbook
// would refuse it, and the component still names the Betrokkene by something
// it can vouch for.
func TestSubjectFallsBackToALocalPseudonym(t *testing.T) {
	client, err := New(testConfig("http://logboek:4016"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for name, header := range map[string]http.Header{
		"no headers":      {},
		"unknown type":    {HeaderSubjectID: []string{"X-1"}, HeaderSubjectIDType: []string{"bsn"}},
		"type without id": {HeaderSubjectIDType: []string{SubjectTypePI}},
	} {
		t.Run(name, func(t *testing.T) {
			id, idType := client.Subject(header, "123456789")
			if idType != SubjectTypePseudonym || !strings.HasPrefix(id, "LP-") {
				t.Fatalf("Subject = (%q, %q), want a logbook-local pseudonym", id, idType)
			}
			if strings.Contains(id, "123456789") {
				t.Fatalf("the pseudonym contains the BSN: %q", id)
			}
		})
	}
}

// Stable within one Verantwoordelijke, different across them: a shared key
// would make the same citizen recognisable across organisations' logbooks.
func TestLocalPseudonymIsStablePerKey(t *testing.T) {
	first, err := New(testConfig("http://logboek:4016"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	cfg := testConfig("http://logboek:4016")
	cfg.PseudonymKey = "another-verantwoordelijke"
	second, err := New(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	const bsn, otherBSN = "123456789", "999991772"
	firstPseudonym := first.LocalPseudonym(bsn)
	if firstPseudonym != first.LocalPseudonym(bsn) {
		t.Error("the same BSN must map to the same pseudonym within one logbook")
	}
	if firstPseudonym == second.LocalPseudonym(bsn) {
		t.Error("two Verantwoordelijken must not derive the same pseudonym")
	}
	if firstPseudonym == first.LocalPseudonym(otherBSN) {
		t.Error("different BSNs must map to different pseudonyms")
	}
}

func TestForeignProcessorNamesTheCallingPeer(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"sub": "AAAABBBBCCCCDDDDEEEE"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	request.Header.Set("Fsc-Authorization", "Bearer header."+base64.RawURLEncoding.EncodeToString(payload)+".signature")
	if got := ForeignProcessor(request); got != "fsc-peer:AAAABBBBCCCCDDDDEEEE" {
		t.Errorf("ForeignProcessor = %q", got)
	}
}

// Without a peer-shaped claim, the grant hash is the one thing about the
// caller this side can actually verify.
func TestForeignProcessorFallsBackToTheGrantHash(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	request.Header.Set("Fsc-Grant-Hash", "abc123")
	if got := ForeignProcessor(request); got != "fsc-grant:abc123" {
		t.Errorf("ForeignProcessor = %q", got)
	}
}

// A locally initiated processing has no foreign processor, and an absent
// attribute is not the same as an empty one.
func TestForeignProcessorIsEmptyWithoutFSC(t *testing.T) {
	if got := ForeignProcessor(httptest.NewRequest(http.MethodPost, "/graphql", nil)); got != "" {
		t.Errorf("ForeignProcessor = %q, want empty", got)
	}
}

func TestClaimsIgnoresAnUndecodableToken(t *testing.T) {
	for _, token := range []string{"", "Bearer notatoken", "Bearer a.!!!.c"} {
		if claims := Claims(token); claims != nil {
			t.Errorf("Claims(%q) = %v, want nil", token, claims)
		}
	}
}

func TestParentSpanFromHeader(t *testing.T) {
	header := http.Header{}
	if got := ParentSpanFromHeader(header); got != "" {
		t.Errorf("ParentSpanFromHeader = %q, want empty when this component starts the tree", got)
	}
	header.Set(HeaderParentSpanID, " b7ad6b7169203331 ")
	if got := ParentSpanFromHeader(header); got != "b7ad6b7169203331" {
		t.Errorf("ParentSpanFromHeader = %q", got)
	}
}
