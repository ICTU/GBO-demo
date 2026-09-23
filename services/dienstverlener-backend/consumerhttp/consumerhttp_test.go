package consumerhttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gbo-demo/dienstverlener-backend/consumer"
	"gbo-demo/dienstverlener-backend/logbook"
	"gbo-demo/dienstverlener-backend/outway"
	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"
)

type fakeSource struct {
	answer consumer.Answer
	err    error
	asked  []consumer.Question
}

func (s *fakeSource) Ask(_ context.Context, q consumer.Question) (consumer.Answer, error) {
	s.asked = append(s.asked, q)
	return s.answer, s.err
}

type refusingLogbook struct{}

func (refusingLogbook) Begin(context.Context) consumer.Position { return consumer.Position{} }
func (refusingLogbook) Record(context.Context, consumer.Processing) error {
	return errors.New("refused")
}

func testToken(consentID string, scopes ...string) string {
	encode := base64.RawURLEncoding.EncodeToString
	payload, _ := json.Marshal(map[string]any{"consent_id": consentID, "pi": "PI-abc123", "scopes": scopes})
	return encode([]byte(`{"alg":"none"}`)) + "." + encode(payload) + ".sig"
}

func post(t *testing.T, handler http.Handler, body string, header http.Header) (*http.Response, map[string]any) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/dvtp/query", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for key, values := range header {
		req.Header[key] = values
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &out)
	return resp, out
}

func queryBody(fields map[string]any) string {
	encoded, _ := json.Marshal(fields)
	return string(encoded)
}

// Each outcome has its status: a refusal is still a successful exchange,
// everything that is not an answer is not a 200.
func TestEachOutcomeHasItsStatus(t *testing.T) {
	valid := queryBody(map[string]any{"consent_token": testToken("c-1", "bd:ib:2025")})
	for name, tc := range map[string]struct {
		consumer *consumer.Consumer
		body     string
		status   int
		allowed  bool
	}{
		"allowed": {&consumer.Consumer{Kind: consumer.Kinds["bd"], Source: &fakeSource{answer: consumer.Answer{Allowed: true, Data: json.RawMessage(`{}`)}}}, valid, http.StatusOK, true},
		"denied":  {&consumer.Consumer{Kind: consumer.Kinds["bd"], Source: &fakeSource{answer: consumer.Answer{Reason: "CONSENT_WITHDRAWN"}}}, valid, http.StatusOK, false},
		"unreadable token": {&consumer.Consumer{Kind: consumer.Kinds["bd"], Source: &fakeSource{}},
			queryBody(map[string]any{"consent_token": "not-a-jwt"}), http.StatusForbidden, false},
		"unlogged": {&consumer.Consumer{Kind: consumer.Kinds["bd"], Source: &fakeSource{answer: consumer.Answer{Allowed: true}}, Logbook: refusingLogbook{}},
			valid, http.StatusInternalServerError, false},
		"unreachable": {&consumer.Consumer{Kind: consumer.Kinds["bd"], Source: &fakeSource{err: errors.New("fsc_outway_call_failed: refused")}},
			valid, http.StatusBadGateway, false},
	} {
		t.Run(name, func(t *testing.T) {
			resp, out := post(t, NewMux(tc.consumer), tc.body, nil)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d (%v)", resp.StatusCode, tc.status, out)
			}
			if out["allowed"] != tc.allowed {
				t.Fatalf("allowed = %v, want %v", out["allowed"], tc.allowed)
			}
		})
	}
}

func TestARequestThatCannotBeAskedIsABadRequest(t *testing.T) {
	c := &consumer.Consumer{Kind: consumer.Kinds["lvg"], Source: &fakeSource{}}
	for name, body := range map[string]string{
		"not json":         `{`,
		"no consent token": `{}`,
		"no vbo id":        queryBody(map[string]any{"consent_token": testToken("c-ir", "lvg:vbo:eigendom")}),
	} {
		t.Run(name, func(t *testing.T) {
			resp, out := post(t, NewMux(c), body, nil)
			if resp.StatusCode != http.StatusBadRequest || out["error"] == "" {
				t.Fatalf("status = %d, body = %v, want 400 with an error", resp.StatusCode, out)
			}
		})
	}
}

// Every field of the request reaches the core, and the dev-portal marker comes
// from its header.
func TestTheRequestReachesTheCore(t *testing.T) {
	source := &fakeSource{answer: consumer.Answer{Allowed: true}}
	c := &consumer.Consumer{Kind: consumer.Kinds["lvg"], Source: source}

	resp, out := post(t, NewMux(c), queryBody(map[string]any{
		"consent_token": testToken("c-ir", "lvg:vbo:eigendom"),
		"scope_id":      "lvg:vbo:eigendom",
		"vbo_id":        "0632010000099412",
	}), http.Header{"X-Demo-Source": {"dev-portal"}})

	if resp.StatusCode != http.StatusOK || len(source.asked) != 1 {
		t.Fatalf("status = %d, asked %d times", resp.StatusCode, len(source.asked))
	}
	if got := source.asked[0].Variables["vboId"]; got != "0632010000099412" {
		t.Errorf("vboId = %v, want the requested VBO-id", got)
	}
	if tx := source.asked[0].TransactionID; tx == "" || out["fsc_transaction_id"] != tx {
		t.Errorf("transaction id sent %q, reported %v; want one id for both", tx, out["fsc_transaction_id"])
	}
}

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(NewMux(&consumer.Consumer{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
}

// The wiring end to end, through the real Outway and logbook adapters and the
// whole middleware stack: the consumer's record is the root of the chain, and
// the source receives its position and the transaction id the caller is told.
func TestAQueryIsLoggedWhereTheSourceCanFindIt(t *testing.T) {
	const bdLogbook = "https://logboek.belastingdienst.nl/data-processing-operations"
	fake := ldvtest.New(t, consumer.Kinds["bd"].Activity)
	var received http.Header
	outwayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"bsn":"PI-abc123"}}}`))
	}))
	defer outwayServer.Close()
	c := &consumer.Consumer{
		Kind:    consumer.Kinds["bd"],
		Source:  &outway.Source{URL: outwayServer.URL + "/bri/graphql", Client: &http.Client{Timeout: 2 * time.Second}},
		Logbook: &logbook.Logbook{Client: fake.Client(t, "dienstverlener-backend"), NextLogbooks: map[string]string{"bd": bdLogbook}},
	}

	resp, out := post(t, NewHandler(c, "dienstverlener-backend"),
		queryBody(map[string]any{"consent_token": testToken("c-1", "bd:ib:2025"), "belastingjaren": []int{2025}}), nil)

	if resp.StatusCode != http.StatusOK || out["allowed"] != true {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, out)
	}
	records := fake.Written()
	if len(records) != 1 {
		t.Fatalf("wrote %d records, want 1", len(records))
	}
	record := records[0]
	if got := record.Attributes[ldv.AttrNextLogbookID]; got != bdLogbook {
		t.Errorf("nextLogbookId = %v, want the source's read API", got)
	}
	if got := ldv.TraceContextFrom(context.Background(), received, "").TraceID; got != record.TraceID {
		t.Errorf("the source received trace %q, want the record's %q", got, record.TraceID)
	}
	if got := ldv.ParentSpanFor(received, record.TraceID); got != record.SpanID {
		t.Errorf("the source would hang under span %q, want the record's %q", got, record.SpanID)
	}
	if tx := received.Get("Fsc-Transaction-Id"); tx == "" || out["fsc_transaction_id"] != tx {
		t.Errorf("Fsc-Transaction-Id %q, reported %v; want one id for both", tx, out["fsc_transaction_id"])
	}
}
