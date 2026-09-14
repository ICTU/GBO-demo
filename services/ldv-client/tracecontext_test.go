package ldvclient

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// Propagation is OpenTelemetry's, so these tests are about the behaviour this
// package promises — not about re-testing the parser. They exist because the
// hand-written one got three W3C rules wrong, and a regression would be silent.
func TestTraceContextFollowsAnIncomingTraceparent(t *testing.T) {
	header := http.Header{}
	header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")

	traceContext := TraceContextFrom(context.Background(), header, "00f067aa0ba902b7")
	if traceContext.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("TraceID = %q", traceContext.TraceID)
	}
	if traceContext.SpanID != "00f067aa0ba902b7" {
		t.Errorf("SpanID = %q, want the span the caller supplied", traceContext.SpanID)
	}
	if !traceContext.Sampled {
		t.Error("the sampled flag was not read")
	}
}

// Bit 0 of trace-flags is sampled. The old code treated any non-zero byte as
// sampled, which is a different question — flags 02 is not sampled.
func TestSampledIsBitZeroOfTraceFlags(t *testing.T) {
	for flags, want := range map[string]bool{
		"00": false,
		"01": true,
		"02": false, // a flag we do not know, with sampled unset
		"03": true,  // that same flag, with sampled set
	} {
		t.Run(flags, func(t *testing.T) {
			header := http.Header{}
			header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-"+flags)
			if got := TraceContextFrom(context.Background(), header, "00f067aa0ba902b7").Sampled; got != want {
				t.Fatalf("flags %s -> Sampled = %v, want %v", flags, got, want)
			}
		})
	}
}

// A higher version is parsed for the fields it still defines rather than
// rejected: W3C says a future version keeps traceparent's leading fields, so
// discarding it would lose a correlation that was there to be had.
func TestAHigherVersionIsStillFollowed(t *testing.T) {
	header := http.Header{}
	header.Set("traceparent", "01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-something-added-later")

	if got := TraceContextFrom(context.Background(), header, "00f067aa0ba902b7").TraceID; got != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("TraceID = %q; a higher version must still yield its trace id", got)
	}
}

// tracestate travels alongside traceparent. Dropping it silently discards
// other vendors' correlation on every hop.
func TestTracestateSurvivesTheHop(t *testing.T) {
	incoming := http.Header{}
	incoming.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	incoming.Set("tracestate", "vendorname=opaqueValue,other=thing")

	traceContext := TraceContextFrom(context.Background(), incoming, "00f067aa0ba902b7")
	if traceContext.State == "" {
		t.Fatal("tracestate was not read")
	}

	outgoing := http.Header{}
	InjectTraceparent(outgoing, traceContext)
	if got := outgoing.Get("tracestate"); got == "" {
		t.Fatalf("tracestate was not propagated; W3C requires both headers to travel")
	}
	if got := outgoing.Get("traceparent"); got == "" {
		t.Fatal("traceparent was not injected")
	}
}

// A hop without traceparent falls back to the transaction id. It is a UUID,
// so it is also a valid 16-byte trace id.
func TestTheFscTransactionIDIsTheFallback(t *testing.T) {
	header := http.Header{}
	header.Set("Fsc-Transaction-Id", "0af76519-16cd-43dd-8448-eb211c80319c")

	traceContext := TraceContextFrom(context.Background(), header, "00f067aa0ba902b7")
	if traceContext.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("TraceID = %q, want the transaction id with the hyphens removed", traceContext.TraceID)
	}
}

// LDV §3.3.1: an action started by another takes the caller's trace over
// unchanged. FSC gives every transaction its own id, so preferring it would
// split one processing into as many traces as it has FSC hops; the ADL links
// the two ids instead.
func TestACallersTraceparentBeatsTheFscTransactionID(t *testing.T) {
	header := http.Header{}
	header.Set("Fsc-Transaction-Id", "0af76519-16cd-43dd-8448-eb211c80319c")
	header.Set("traceparent", "00-11111111111111111111111111111111-b7ad6b7169203331-01")

	got := TraceContextFrom(context.Background(), header, "00f067aa0ba902b7").TraceID
	if got != "11111111111111111111111111111111" {
		t.Errorf("TraceID = %q, want the caller's trace", got)
	}
}

// X-Request-Id is a fallback as well; it never beats a traceparent.
func TestTraceparentWinsWhenThereIsNoFscTransaction(t *testing.T) {
	header := http.Header{}
	header.Set("traceparent", "00-11111111111111111111111111111111-b7ad6b7169203331-01")
	header.Set("X-Request-Id", "0af76519-16cd-43dd-8448-eb211c80319c")

	got := TraceContextFrom(context.Background(), header, "00f067aa0ba902b7").TraceID
	if got != "11111111111111111111111111111111" {
		t.Errorf("TraceID = %q, want the traceparent", got)
	}
}

// A malformed traceparent is worse than none, so an unusable context injects
// nothing at all.
func TestInjectSkipsAnUnusableContext(t *testing.T) {
	for name, traceContext := range map[string]TraceContext{
		"no ids":       {},
		"no span":      {TraceID: "0af7651916cd43dd8448eb211c80319c"},
		"zero trace":   {TraceID: "00000000000000000000000000000000", SpanID: "b7ad6b7169203331"},
		"zero span":    {TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "0000000000000000"},
		"bad trace id": {TraceID: "nope", SpanID: "b7ad6b7169203331"},
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{}
			InjectTraceparent(header, traceContext)
			if got := header.Get("traceparent"); got != "" {
				t.Fatalf("injected %q", got)
			}
			if got := traceContext.Traceparent(); got != "" {
				t.Fatalf("Traceparent() = %q, want empty rather than malformed", got)
			}
		})
	}
}

func TestParentSpanFor(t *testing.T) {
	const callersTrace = "0af7651916cd43dd8448eb211c80319c"

	header := http.Header{}
	if got := ParentSpanFor(header, callersTrace); got != "" {
		t.Errorf("ParentSpanFor = %q, want empty when this component starts the tree", got)
	}

	header.Set("traceparent", "00-"+callersTrace+"-b7ad6b7169203331-01")
	if got := ParentSpanFor(header, callersTrace); got != "b7ad6b7169203331" {
		t.Errorf("ParentSpanFor = %q, want the caller's span", got)
	}

	// The caller was in another trace, so its span is not this record's
	// parent. Naming it anyway would produce a parent no reader of this
	// logbook can resolve.
	if got := ParentSpanFor(header, "4bf92f3577b34da6a3ce929d0e0e4736"); got != "" {
		t.Errorf("ParentSpanFor = %q, want empty when the caller was in another trace", got)
	}

	// Case is not part of the identity: a hex id is the same id either way.
	if got := ParentSpanFor(header, strings.ToUpper(callersTrace)); got != "b7ad6b7169203331" {
		t.Errorf("ParentSpanFor = %q, want the caller's span regardless of hex case", got)
	}
}

func TestIDValidation(t *testing.T) {
	if !IsTraceID("0af7651916cd43dd8448eb211c80319c") || !IsSpanID("b7ad6b7169203331") {
		t.Error("valid ids rejected")
	}
	// The all-zero ids are invalid per the specification.
	if IsTraceID("00000000000000000000000000000000") || IsSpanID("0000000000000000") {
		t.Error("all-zero ids must not be treated as usable")
	}
	if IsTraceID("short") || IsSpanID("short") {
		t.Error("malformed ids accepted")
	}
}

// The regression this ordering exists to prevent, and it is not hypothetical:
// behind otelhttp every request already carries a locally created server span.
// Extracting against the live context returns that span when the headers hold
// no traceparent, so the Fsc-Transaction-Id was never consulted and every
// component filed its records under a trace id of its own.
func TestTheFscFallbackBeatsAnAmbientSpan(t *testing.T) {
	ambient := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x11},
		SpanID:     trace.SpanID{0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), ambient)

	// The hop carried no traceparent; only the transaction id arrived.
	header := http.Header{}
	header.Set("Fsc-Transaction-Id", "0af76519-16cd-43dd-8448-eb211c80319c")

	if got := TraceContextFrom(ctx, header, "00f067aa0ba902b7").TraceID; got != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("TraceID = %q, want the Fsc-Transaction-Id rather than the local span", got)
	}
}

// The ambient span is the fallback of last resort, not of first: with no
// header at all it is better than a fresh id, because it at least ties the
// record to the trace this process is serving.
func TestTheAmbientSpanIsUsedWhenNoHeaderCarriesATrace(t *testing.T) {
	ambient := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33, 0x33},
		SpanID:     trace.SpanID{0x44, 0x44, 0x44, 0x44, 0x44, 0x44, 0x44, 0x44},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), ambient)

	if got := TraceContextFrom(ctx, http.Header{}, "00f067aa0ba902b7").TraceID; got != "33333333333333333333333333333333" {
		t.Fatalf("TraceID = %q, want the ambient span", got)
	}
	// And a traceparent still beats it.
	header := http.Header{}
	header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	if got := TraceContextFrom(ctx, header, "00f067aa0ba902b7").TraceID; got != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("TraceID = %q, want the traceparent to win over the ambient span", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func capturing(sent *http.Header) http.RoundTripper {
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*sent = r.Header.Clone()
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
	})
}

// An instrumented transport injects its own client span after the caller set
// the traceparent, so the callee would hang its records under a span no
// logbook holds. Transport, placed inside the instrumentation, puts the LDV
// position back on the way out.
func TestTransportRestoresThePositionAfterInstrumentation(t *testing.T) {
	position := TraceContext{TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331", Sampled: true}
	var sent http.Header
	inner := Transport(capturing(&sent))
	// What otelhttp does: inject the client span it has just started.
	instrumented := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("traceparent", "00-0af7651916cd43dd8448eb211c80319c-1111111111111111-01")
		return inner.RoundTrip(r)
	})

	request, err := http.NewRequest(http.MethodGet, "http://source.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instrumented.RoundTrip(CarryTraceparent(request, position)); err != nil {
		t.Fatal(err)
	}
	if got := ParentSpanFor(sent, position.TraceID); got != position.SpanID {
		t.Errorf("the callee would hang under span %q, want the LDV record's %q", got, position.SpanID)
	}
}

// Without a position on the context the transport leaves the request alone.
func TestTransportLeavesARequestWithoutAPositionAlone(t *testing.T) {
	const traceparent = "00-0af7651916cd43dd8448eb211c80319c-1111111111111111-01"
	var sent http.Header
	request, err := http.NewRequest(http.MethodGet, "http://source.test/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("traceparent", traceparent)
	if _, err := Transport(capturing(&sent)).RoundTrip(request); err != nil {
		t.Fatal(err)
	}
	if got := sent.Get("traceparent"); got != traceparent {
		t.Errorf("traceparent = %q, want it untouched", got)
	}
}
