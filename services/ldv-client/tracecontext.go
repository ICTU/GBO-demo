package ldvclient

import (
	"net/http"
	"regexp"
	"strings"
)

// W3C Trace Context. §3.1 of the standard is unambiguous: when HTTP/1.1 or
// HTTP/2 carries a dataverwerking across applications, Trace Context MUST be
// used. So the chain correlates on `traceparent`, not on headers of our own
// invention.
//
// The one place that cannot hold is the FSC hop. FSC v2.4.0 does not forward
// `traceparent` between peers, and no amount of care on our side changes what
// the Inway strips. There the chain falls back to the Fsc-Transaction-Id,
// which is a UUID and therefore exactly a 16-byte trace-id once the hyphens
// come off — so the same value continues on the far side, reachable through a
// header FSC does propagate.
//
// That fallback is a profile deviation, not conformance, and is written up as
// one in the logbook README. Naming it is the point: a reader must be able to
// tell where the chain follows the standard and where it works around a
// transport that cannot.

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
}

var traceparentPattern = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)

// zeroTraceID and zeroSpanID are invalid per the specification; a traceparent
// carrying either must be treated as absent rather than followed.
const (
	zeroTraceID = "00000000000000000000000000000000"
	zeroSpanID  = "0000000000000000"
)

// ParseTraceparent reads a W3C traceparent header. Only version 00 is
// accepted: a later version may reorder the fields, and guessing at a format
// we do not know would silently corrupt the correlation.
func ParseTraceparent(value string) (TraceContext, bool) {
	match := traceparentPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return TraceContext{}, false
	}
	if match[1] == zeroTraceID || match[2] == zeroSpanID {
		return TraceContext{}, false
	}
	return TraceContext{TraceID: match[1], SpanID: match[2], Sampled: match[3] != "00"}, true
}

// Traceparent renders the header. Empty when the context is not usable, so a
// caller never sends a malformed one.
func (t TraceContext) Traceparent() string {
	if !IsTraceID(t.TraceID) || !IsSpanID(t.SpanID) {
		return ""
	}
	flags := "00"
	if t.Sampled {
		flags = "01"
	}
	return "00-" + t.TraceID + "-" + t.SpanID + "-" + flags
}

// TraceContextFrom reads the incoming request's position in the trace.
//
// A standard traceparent wins. Failing that the Fsc-Transaction-Id is used —
// the FSC hop strips traceparent, so on the far side that header is all
// there is. The span is this component's own record, which the caller sets.
func TraceContextFrom(header http.Header, spanID string) TraceContext {
	if parsed, ok := ParseTraceparent(header.Get("traceparent")); ok {
		return TraceContext{TraceID: parsed.TraceID, SpanID: spanID, Sampled: parsed.Sampled}
	}
	for _, name := range []string{"Fsc-Transaction-Id", "X-Request-Id"} {
		if candidate := NormalizeTraceID(header.Get(name)); candidate != "" {
			return TraceContext{TraceID: candidate, SpanID: spanID, Sampled: true}
		}
	}
	return TraceContext{SpanID: spanID, Sampled: true}
}

// InjectTraceparent puts this component's position on an outgoing request, so
// the next application continues the same trace. Called for every hop between
// applications that perform a dataverwerking (§3.1).
func InjectTraceparent(header http.Header, trace TraceContext) {
	if value := trace.Traceparent(); value != "" {
		header.Set("traceparent", value)
	}
}

// IsSpanID reports whether a value is a usable W3C span id.
func IsSpanID(value string) bool {
	return len(value) == 16 && value != zeroSpanID && spanIDPattern.MatchString(value)
}

var spanIDPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)
