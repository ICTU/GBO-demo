package ldvclient

import (
	"context"
	"net/http"
	"strings"

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
// The FSC hop is where that stops being sufficient — though not for the reason
// first assumed. `traceparent` is in fact forwarded across the demo's
// outway/inway pair and arrives intact on the far side; what does not survive
// is its authority. FSC gives every transaction its own Fsc-Transaction-Id, a
// UUID v7 it validates strictly and rejects anything else for, so it cannot be
// derived from a caller's trace id. Where a caller brings its own
// `traceparent`, a request therefore carries two unrelated 128-bit ids at
// once, and only one of them — the transaction id — is the one FSC's txlog and
// the PDP's decision log record.
//
// So the transaction id wins whenever there is one. It is a UUID, and
// therefore exactly a 16-byte trace-id once the hyphens come off, which is
// what makes it usable as one.
//
// Preferring a header of FSC's invention over `traceparent` is a profile
// deviation, not conformance, and is written up as one in the logbook README.

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
//	Fsc-Transaction-Id  →  traceparent on the request  →  X-Request-Id
//	→  the ambient span  →  a fresh trace
//
// The FSC transaction id comes first because it, not `traceparent`, is the
// identifier the three logs actually share. The standard ties LDV, the
// authorization decision log and FSC-Logging together by one trace id, and
// FSC's txlog and the PDP's decision both key on the transaction id. A record
// filed under anything else is unreachable from the other two.
//
// The two ids are not interchangeable and cannot be made so. FSC validates the
// transaction id as a UUID v7 and rejects the request outright otherwise
// ("invalid uuid version, must be v7"), so a caller's trace id can never
// become the transaction id. Where a caller supplies its own `traceparent` —
// any browser with OTel instrumentation does — the two therefore differ by
// construction, and preferring `traceparent` files LDV records under an id the
// decision log and the txlog have never heard of.
//
// `traceparent` still wins wherever there is no FSC transaction: inside one
// Verantwoordelijke's own chain that is the only correlator there is, and W3C
// Trace Context §3.1 requires continuing it.
//
// The ambient span comes last. Behind otelhttp every request already carries a
// locally created server span, so asking the context earlier would always
// answer and no header would ever be reached — every component would file its
// records under a trace id of its own, which is precisely the chain view this
// exists to make possible.
//
// Hence the extraction runs against context.Background(): the propagator
// returns its input unchanged when the carrier holds no valid traceparent, so
// handing it the live context would let the ambient span through disguised as
// an extracted one.
func TraceContextFrom(ctx context.Context, header http.Header, spanID string) TraceContext {
	if candidate := NormalizeTraceID(header.Get("Fsc-Transaction-Id")); candidate != "" {
		return TraceContext{TraceID: candidate, SpanID: spanID, Sampled: true}
	}
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
	if candidate := NormalizeTraceID(header.Get("X-Request-Id")); candidate != "" {
		return TraceContext{TraceID: candidate, SpanID: spanID, Sampled: true}
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

// ParentSpanFor returns the span the caller was in, so this record hangs under
// it — but only when the caller was in the same trace as this record.
//
// §3.1 states the two together: an action started by another action takes the
// `trace_id` over unchanged *and* records that action's `span_id` as
// `parent_span_id`. They are one rule, so where they come apart they must not
// be combined. That happens on exactly one hop here: a caller brings its own
// `traceparent` while the record is filed under the Fsc-Transaction-Id, which
// is the id the source, the txlog and the decision log will use. The header's
// span then belongs to a different trace, and a `parent_span_id` pointing into
// it names a parent no reader of this logbook can resolve — the record looks
// nested but hangs from nothing. A root record is the honest answer.
//
// Empty when this component starts the tree, when the hop that delivered the
// request carried no trace context, or when the caller was in another trace.
func ParentSpanFor(header http.Header, traceID string) string {
	extracted := trace.SpanContextFromContext(
		propagator.Extract(context.Background(), propagation.HeaderCarrier(header)),
	)
	if !extracted.HasSpanID() {
		return ""
	}
	if !strings.EqualFold(extracted.TraceID().String(), strings.TrimSpace(traceID)) {
		return ""
	}
	return extracted.SpanID().String()
}

// IsSpanID reports whether a value is a usable W3C span id.
func IsSpanID(value string) bool {
	spanID, err := trace.SpanIDFromHex(value)
	return err == nil && spanID.IsValid()
}
