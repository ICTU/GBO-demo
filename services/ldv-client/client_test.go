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
	// A standard traceparent wins over the FSC fallback, because that is what
	// the standard says to correlate on where the transport allows it.
	header.Set("traceparent", "00-11111111111111111111111111111111-b7ad6b7169203331-01")
	if got := TraceID(context.Background(), header); got != "11111111111111111111111111111111" {
		t.Errorf("TraceID = %q, want the traceparent", got)
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
func TestForeignProcessorNamesTheCallingPeer(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"sub": "AAAABBBBCCCCDDDDEEEE"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	client, err := New(testConfig("http://logboek:4016"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	request.Header.Set("Fsc-Authorization", "Bearer header."+base64.RawURLEncoding.EncodeToString(payload)+".signature")
	// §3.2.2.9 wants a URL, and an FSC peer id is not one.
	if got, want := client.ForeignProcessor(request), DefaultPeerURIBase+"/AAAABBBBCCCCDDDDEEEE"; got != want {
		t.Errorf("ForeignProcessor = %q, want %q", got, want)
	}
}

// Without a peer-shaped claim, the grant hash is the one thing about the
// caller this side can actually verify.
func TestForeignProcessorFallsBackToTheGrantHash(t *testing.T) {
	client, err := New(testConfig("http://logboek:4016"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", nil)
	request.Header.Set("Fsc-Grant-Hash", "abc123")
	if got, want := client.ForeignProcessor(request), DefaultPeerURIBase+"/by-grant/abc123"; got != want {
		t.Errorf("ForeignProcessor = %q, want %q", got, want)
	}
}

// A locally initiated processing has no foreign processor, and an absent
// attribute is not the same as an empty one.
func TestForeignProcessorIsEmptyWithoutFSC(t *testing.T) {
	client, err := New(testConfig("http://logboek:4016"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := client.ForeignProcessor(httptest.NewRequest(http.MethodPost, "/graphql", nil)); got != "" {
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
	header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	if got := ParentSpanFromHeader(header); got != "b7ad6b7169203331" {
		t.Errorf("ParentSpanFromHeader = %q, want the caller's span", got)
	}
}
