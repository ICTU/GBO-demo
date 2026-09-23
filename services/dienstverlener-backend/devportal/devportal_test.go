package devportal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gbo-demo/dienstverlener-backend/consumer"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type captured struct {
	traceparent string
	body        map[string]any
}

func devPortal(t *testing.T) (*History, <-chan captured) {
	t.Helper()
	got := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history" {
			t.Errorf("path = %q, want /history", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		got <- captured{traceparent: r.Header.Get("Traceparent"), body: body}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return &History{Base: server.URL}, got
}

func run(traceID string) consumer.Run {
	return consumer.Run{
		Request: consumer.Request{ConsentToken: "secret-consent-token", ScopeID: "bd:ib:2025", Belastingjaren: []int{2025}},
		Result:  consumer.Result{Outcome: consumer.Allowed, ConsentID: "c-1", TraceID: traceID, TransactionID: "tx-1"},
	}
}

func receive(t *testing.T, got <-chan captured) captured {
	t.Helper()
	select {
	case c := <-got:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("the dev-portal never received the history post")
		return captured{}
	}
}

// The post stays attached to the run that produced it, and it replays the run
// without the consent token.
func TestAHistoryPostCarriesTheTrace(t *testing.T) {
	h, got := devPortal(t)
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	ctx, span := tp.Tracer("test").Start(context.Background(), "dvtp.query")
	defer span.End()
	traceID := span.SpanContext().TraceID().String()

	h.Observe(ctx, run(traceID))

	c := receive(t, got)
	if !strings.Contains(c.traceparent, traceID) {
		t.Errorf("traceparent = %q, want trace %s", c.traceparent, traceID)
	}
	if c.body["trace_id"] != traceID || c.body["outcome"] != "allow" || c.body["consent_id"] != "c-1" {
		t.Errorf("body = %v", c.body)
	}
	// The dev-portal renders the stored response as the API's own.
	response, _ := c.body["response"].(map[string]any)
	if response["allowed"] != true || response["fsc_transaction_id"] != "tx-1" {
		t.Errorf("response = %v, want the API response", response)
	}
	encoded, _ := json.Marshal(c.body)
	if strings.Contains(string(encoded), "secret-consent-token") {
		t.Fatalf("history payload leaked the consent token: %s", encoded)
	}
}

// The post outlives the handler: the request's cancellation does not reach it.
func TestAHistoryPostSurvivesTheHandlerReturning(t *testing.T) {
	h, got := devPortal(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.Observe(ctx, run(""))
	cancel()

	receive(t, got)
}

// A run the dev-portal drove is already on its timeline.
func TestADevPortalRunIsNotPostedTwice(t *testing.T) {
	h, got := devPortal(t)
	r := run("")
	r.Request.FromDevPortal = true

	h.Observe(context.Background(), r)

	select {
	case c := <-got:
		t.Fatalf("posted a dev-portal run: %v", c.body)
	case <-time.After(200 * time.Millisecond):
	}
}
