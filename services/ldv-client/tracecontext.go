package ldvclient

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// W3C Trace Context. §3.1 of the standard is unambiguous: when HTTP/1.1 or
// HTTP/2 carries a dataverwerking across applications, Trace Context MUST be
// used. So the chain correlates on `traceparent`, not on headers of our own
// invention.
//
// Propagation is OpenTelemetry's, not ours. A hand-written parser had to get
// the versioning rule right (a higher version is parsed for the fields it
// still defines, not rejected), the sampled flag right (bit 0 of trace-flags,
// not "the byte is non-zero"), and `tracestate` carried alongside — three
// things it got wrong, and three things that are somebody else's solved
// problem. The service already depends on OTel; using its propagator deletes
// the edge cases rather than fixing them one at a time.
//
// The one place Trace Context cannot hold is the FSC hop. FSC v2.4.0 does not
// forward `traceparent` between peers, and no amount of care on our side
// changes what the Inway strips. There the chain falls back to the
// Fsc-Transaction-Id, which is a UUID and therefore exactly a 16-byte trace-id
// once the hyphens come off — so the same value continues on the far side,
// reachable through a header FSC does propagate.
//
// That fallback is a profile deviation, not conformance, and is written up as
// one in the logbook README.

// propagator handles traceparent and tracestate together, as W3C requires.
var propagator = propagation.TraceContext{}

// TraceContext is one request's position in a trace.
type TraceContext struct {
	// TraceID identifies the request across every application it touches.
	TraceID string
	// SpanID is the caller's span — the parent of whatever the callee does.
	SpanID string
	// Sampled carries the W3C sampled flag. LDV records are never sampled;
	// this only travels so the flag survives the hop for anything that does
	// use it.
	Sampled bool
	// State is the W3C tracestate, carried through untouched. Dropping it
	// would silently discard other vendors' correlation on every hop.
	State string
}

// TraceContextFrom reads the incoming request's position in the trace, in a
// strict order:
//
//	traceparent on the request  →  Fsc-Transaction-Id  →  X-Request-Id
//	→  the ambient span  →  a fresh trace
//
// The ambient span comes last, and that ordering is the whole point. Behind
// otelhttp every request already carries a locally created server span, so
// asking the context first would always answer — and the FSC fallback, the one
// thing that carries correlation across a hop that strips traceparent, would
// never be reached. Every component would then file its records under a trace
// id of its own, which is precisely the chain view this exists to make
// possible.
//
// Hence the extraction runs against context.Background(): the propagator
// returns its input unchanged when the carrier holds no valid traceparent, so
// handing it the live context would let the ambient span through disguised as
// an extracted one.
func TraceContextFrom(ctx context.Context, header http.Header, spanID string) TraceContext {
	fromHeader := trace.SpanContextFromContext(
		propagator.Extract(context.Background(), propagation.HeaderCarrier(header)),
	)
	if fromHeader.HasTraceID() {
		return TraceContext{
			TraceID: fromHeader.TraceID().String(),
			SpanID:  spanID,
			Sampled: fromHeader.IsSampled(),
			State:   fromHeader.TraceState().String(),
		}
	}
	for _, name := range []string{"Fsc-Transaction-Id", "X-Request-Id"} {
		if candidate := NormalizeTraceID(header.Get(name)); candidate != "" {
			return TraceContext{TraceID: candidate, SpanID: spanID, Sampled: true}
		}
	}
	if ambient := trace.SpanContextFromContext(ctx); ambient.HasTraceID() {
		return TraceContext{
			TraceID: ambient.TraceID().String(),
			SpanID:  spanID,
			Sampled: ambient.IsSampled(),
			State:   ambient.TraceState().String(),
		}
	}
	return TraceContext{SpanID: spanID, Sampled: true}
}

// spanContext turns this into something OTel can inject. Invalid ids yield an
// unusable context, which Inject then skips — a malformed traceparent is worse
// than none.
func (t TraceContext) spanContext() trace.SpanContext {
	traceID, err := trace.TraceIDFromHex(t.TraceID)
	if err != nil {
		return trace.SpanContext{}
	}
	spanID, err := trace.SpanIDFromHex(t.SpanID)
	if err != nil {
		return trace.SpanContext{}
	}
	var flags trace.TraceFlags
	if t.Sampled {
		flags = trace.FlagsSampled
	}
	state, err := trace.ParseTraceState(t.State)
	if err != nil {
		// An unparseable tracestate is dropped rather than propagated
		// malformed; the traceparent, which is what correlates, survives.
		state = trace.TraceState{}
	}
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: flags, TraceState: state,
	})
}

// Traceparent renders the header. Empty when the context is not usable.
func (t TraceContext) Traceparent() string {
	spanContext := t.spanContext()
	if !spanContext.IsValid() {
		return ""
	}
	header := http.Header{}
	propagator.Inject(
		trace.ContextWithSpanContext(context.Background(), spanContext),
		propagation.HeaderCarrier(header),
	)
	return header.Get("traceparent")
}

// InjectTraceparent puts this component's position on an outgoing request, so
// the next application continues the same trace. Injects `tracestate` too,
// because W3C requires both to travel.
func InjectTraceparent(header http.Header, trace TraceContext) {
	spanContext := trace.spanContext()
	if !spanContext.IsValid() {
		return
	}
	propagator.Inject(
		contextWithSpan(spanContext),
		propagation.HeaderCarrier(header),
	)
}

func contextWithSpan(spanContext trace.SpanContext) context.Context {
	return trace.ContextWithSpanContext(context.Background(), spanContext)
}

// ParentSpanFromHeader returns the span the caller was in, so this record
// hangs under it. Empty when this component starts the tree, or when the hop
// that delivered the request dropped the trace context.
func ParentSpanFromHeader(header http.Header) string {
	extracted := trace.SpanContextFromContext(
		propagator.Extract(context.Background(), propagation.HeaderCarrier(header)),
	)
	if !extracted.HasSpanID() {
		return ""
	}
	return extracted.SpanID().String()
}

// IsSpanID reports whether a value is a usable W3C span id.
func IsSpanID(value string) bool {
	spanID, err := trace.SpanIDFromHex(value)
	return err == nil && spanID.IsValid()
}
