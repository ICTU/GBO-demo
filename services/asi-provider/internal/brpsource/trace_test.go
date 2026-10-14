package brpsource

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"gbo-demo/asi-provider/internal/tracing"
)

// recordSpans installs a tracer provider that records every ended span.
func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	prev, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTracerProvider(prev); otel.SetTextMapPropagator(prevProp) })
	return rec
}

// serve runs one request to the ASIP that asks the BRP for names, as the
// handler does, inside the tracing middleware.
func serve(t *testing.T, s *Source, traceparent string, names ...string) {
	t.Helper()
	handler := tracing.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.Values(r.Context(), bsn, names); err != nil {
			t.Errorf("Values: %v", err)
		}
	}), "asi-provider")
	req := httptest.NewRequest(http.MethodPost, "/brp/authsrc-api/verify", strings.NewReader(`{}`))
	if traceparent != "" {
		req.Header.Set("Traceparent", traceparent)
	}
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

// One request: one trace, one query and one FSC transaction. The query's
// span names the transaction the Outway gets, and the traceparent the
// Outway gets is in that trace; without one from the caller, the trace id
// is the transaction id (C3).
func TestOneTraceAndOneTransactionPerRequest(t *testing.T) {
	rec := recordSpans(t)
	o, s := newOutway(t, http.StatusOK, brpPerson(frouke))
	serve(t, s, "", "family_name", "address")

	if o.requests != 1 {
		t.Fatalf("%d requests to the Outway, want one", o.requests)
	}
	tx := o.got.Header.Get("Fsc-Transaction-Id")
	traceID := strings.ReplaceAll(tx, "-", "")
	if parts := strings.Split(o.got.Header.Get("Traceparent"), "-"); len(parts) != 4 || parts[1] != traceID {
		t.Errorf("traceparent to the Outway = %q, want trace id %s", o.got.Header.Get("Traceparent"), traceID)
	}
	var query sdktrace.ReadOnlySpan
	for _, span := range rec.Ended() {
		if span.SpanContext().TraceID().String() != traceID {
			t.Errorf("span %q is in trace %s, want %s", span.Name(), span.SpanContext().TraceID(), traceID)
		}
		if span.Name() == "BRP query" {
			query = span
		}
	}
	if query == nil {
		t.Fatalf("no BRP query span among %d spans", len(rec.Ended()))
	}
	attrs := map[string]string{}
	for _, kv := range query.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	if attrs[tracing.TransactionIDAttribute] != tx || attrs["gbo.asi.attributes"] != `["family_name","address"]` {
		t.Errorf("BRP query attributes = %v, want transaction %s and the two attribute names", attrs, tx)
	}
}

// A caller's traceparent is kept: the request joins the caller's trace, and
// the transaction id is still the ASIP's own.
func TestACallersTraceIsKept(t *testing.T) {
	recordSpans(t)
	o, s := newOutway(t, http.StatusOK, brpPerson(frouke))
	callerTrace := "4bf92f3577b34da6a3ce929d0e0e4736"
	serve(t, s, "00-"+callerTrace+"-00f067aa0ba902b7-01", "family_name")
	if parts := strings.Split(o.got.Header.Get("Traceparent"), "-"); len(parts) != 4 || parts[1] != callerTrace {
		t.Errorf("traceparent to the Outway = %q, want the caller's trace", o.got.Header.Get("Traceparent"))
	}
	if tx := o.got.Header.Get("Fsc-Transaction-Id"); strings.ReplaceAll(tx, "-", "") == callerTrace || tx == "" {
		t.Errorf("Fsc-Transaction-Id = %q, want the ASIP's own", tx)
	}
}

// No span name or attribute holds the BSN or a value the BRP returned (C4).
func TestSpansHoldNoPersonalData(t *testing.T) {
	rec := recordSpans(t)
	_, s := newOutway(t, http.StatusOK, brpPerson(frouke))
	serve(t, s, "", "family_name", "given_name", "birth_date", "nationality", "address")
	personal := []string{bsn, "Jansen", "Frouke", "2000-03-24", "Coolsingel", "3011 AD", "Rotterdam"}
	for _, span := range rec.Ended() {
		texts := []string{span.Name(), span.Status().Description}
		for _, kv := range span.Attributes() {
			texts = append(texts, kv.Value.Emit())
		}
		for _, text := range texts {
			if slices.ContainsFunc(personal, func(p string) bool { return strings.Contains(text, p) }) {
				t.Errorf("span %q holds personal data: %q", span.Name(), text)
			}
		}
	}
	if len(rec.Ended()) < 2 {
		t.Fatalf("%d spans, want the server span, the query and the client call", len(rec.Ended()))
	}
}
