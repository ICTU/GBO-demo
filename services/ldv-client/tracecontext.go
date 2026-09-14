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
// The Fsc-Transaction-Id is not a substitute. FSC forwards `traceparent`
// between peers untouched, but gives every transaction its own id, unique per
// transaction (FSC-Logging §3.4.1.1). One processing that crosses FSC more
// than once — a consumer that calls two sources, a source that calls onwards —
// therefore carries several transaction ids and still one trace id, and LDV
// §3.3.1 and the ADL both require taking that trace id over unchanged. The two
// are linked where the standards link them: the ADL records
// adl.fsc.transaction_id next to trace_id.

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
// `traceparent` comes first because the standard says so: an action started
// by another action takes its trace_id over unchanged (LDV §3.3.1), and W3C
// Trace Context §3.1 requires continuing it.
//
// The transaction id is the fallback for a hop that arrives without one. It
// is a UUID, and therefore exactly a 16-byte trace id once the hyphens come
// off. At the entry of a chain the first component ties its trace to the
// transaction id it mints, so for a request that crosses FSC once the trace
// id, the decision log's transaction id and the txlog carry the same value.
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
	if candidate := NormalizeTraceID(header.Get("Fsc-Transaction-Id")); candidate != "" {
		return TraceContext{TraceID: candidate, SpanID: spanID, Sampled: true}
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
// be combined. With `traceparent` read first they normally cannot come apart,
// because the record's trace id comes from the same header; the check guards a
// component that chose its trace id elsewhere. A `parent_span_id` pointing
// into another trace names a parent no reader of this logbook can resolve —
// the record looks nested but hangs from nothing. A root record is the honest
// answer.
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

// positionKey carries an LDV position on a request's context.
type positionKey struct{}

// CarryTraceparent puts position on the request's traceparent, and on its
// context for Transport. A request sent through an OTel-instrumented transport
// gets that transport's own client span injected over the header; Transport,
// placed inside the instrumentation, puts the LDV position back.
func CarryTraceparent(request *http.Request, position TraceContext) *http.Request {
	InjectTraceparent(request.Header, position)
	return request.WithContext(context.WithValue(request.Context(), positionKey{}, position))
}

// Transport wraps base so a request carrying an LDV position leaves with that
// position on its traceparent, whatever an outer transport injected. Place it
// inside the instrumentation — otelhttp.NewTransport(ldv.Transport(base)) — so
// it runs last: the OTel client span is still recorded, but the callee's
// records hang under the LDV record that made the call, which is the parent a
// reader of the logbook can resolve (§3.3.1).
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return positionTransport{base: base}
}

type positionTransport struct{ base http.RoundTripper }

func (t positionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if position, ok := request.Context().Value(positionKey{}).(TraceContext); ok {
		// A RoundTripper may not modify the request it was given.
		request = request.Clone(request.Context())
		InjectTraceparent(request.Header, position)
	}
	return t.base.RoundTrip(request)
}
