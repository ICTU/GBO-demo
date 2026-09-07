package ldvclient

import (
	"net/http"
	"testing"
)

// §3.1 makes Trace Context mandatory for HTTP between applications, so the
// header has to be parsed and produced exactly, not approximately.
func TestParseTraceparent(t *testing.T) {
	valid := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	parsed, ok := ParseTraceparent(valid)
	if !ok {
		t.Fatalf("ParseTraceparent(%q) rejected a valid header", valid)
	}
	if parsed.TraceID != "0af7651916cd43dd8448eb211c80319c" || parsed.SpanID != "b7ad6b7169203331" || !parsed.Sampled {
		t.Fatalf("parsed = %#v", parsed)
	}
	if got := parsed.Traceparent(); got != valid {
		t.Errorf("round-trip = %q, want %q", got, valid)
	}

	for name, header := range map[string]string{
		"empty":          "",
		"no version":     "0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"short trace id": "00-0af7651916cd43dd-b7ad6b7169203331-01",
		"short span id":  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b71-01",
		"uppercase":      "00-0AF7651916CD43DD8448EB211C80319C-b7ad6b7169203331-01",
		"trailing junk":  "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01-extra",
		// All-zero ids are invalid per the specification: treating one as a
		// real trace would file every such request under one id.
		"zero trace id": "00-" + zeroTraceID + "-b7ad6b7169203331-01",
		"zero span id":  "00-0af7651916cd43dd8448eb211c80319c-" + zeroSpanID + "-01",
		// A later version may reorder the fields; guessing would corrupt the
		// correlation rather than lose it.
		"future version": "01-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := ParseTraceparent(header); ok {
				t.Fatalf("ParseTraceparent(%q) accepted an unusable header", header)
			}
		})
	}
}

func TestTraceparentIsEmptyWhenUnusable(t *testing.T) {
	for name, trace := range map[string]TraceContext{
		"no ids":       {},
		"no span":      {TraceID: "0af7651916cd43dd8448eb211c80319c"},
		"zero span":    {TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: zeroSpanID},
		"bad trace id": {TraceID: "nope", SpanID: "b7ad6b7169203331"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := trace.Traceparent(); got != "" {
				t.Fatalf("Traceparent() = %q, want empty rather than malformed", got)
			}
		})
	}
	unsampled := TraceContext{TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331"}
	if got := unsampled.Traceparent(); got[len(got)-2:] != "00" {
		t.Errorf("an unsampled context should carry flags 00, got %q", got)
	}
}

// The FSC hop strips traceparent, so on the far side the Fsc-Transaction-Id is
// all there is — and it carries the same value, being a UUID. That fallback is
// the documented profile deviation.
func TestTraceContextFallsBackToTheFscTransactionID(t *testing.T) {
	header := http.Header{}
	header.Set("Fsc-Transaction-Id", "0af76519-16cd-43dd-8448-eb211c80319c")

	trace := TraceContextFrom(header, "00f067aa0ba902b7")
	if trace.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("TraceID = %q, want the transaction id with the hyphens removed", trace.TraceID)
	}
	if trace.SpanID != "00f067aa0ba902b7" {
		t.Errorf("SpanID = %q, want the span the caller supplied", trace.SpanID)
	}

	// A real traceparent wins over the fallback.
	header.Set("traceparent", "00-11111111111111111111111111111111-b7ad6b7169203331-01")
	if got := TraceContextFrom(header, "00f067aa0ba902b7").TraceID; got != "11111111111111111111111111111111" {
		t.Errorf("TraceID = %q, want the traceparent to win", got)
	}
}

func TestInjectTraceparentSkipsAnUnusableContext(t *testing.T) {
	header := http.Header{}
	InjectTraceparent(header, TraceContext{})
	if got := header.Get("traceparent"); got != "" {
		t.Fatalf("injected %q; a malformed traceparent is worse than none", got)
	}
	InjectTraceparent(header, TraceContext{
		TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331", Sampled: true,
	})
	if got := header.Get("traceparent"); got != "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01" {
		t.Fatalf("traceparent = %q", got)
	}
}
