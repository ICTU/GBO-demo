package ldvclient

import (
	"encoding/json"
	"testing"
	"time"
)

// The wire format is the contract with every other implementation of the
// standard, so it is asserted against the raw JSON rather than through a
// round-trip that would agree with itself whatever it did.
func TestRecordMarshalsToTheLDVWireFormat(t *testing.T) {
	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	record := Record{
		TraceID:      "0af7651916cd43dd8448eb211c80319c",
		SpanID:       "b7ad6b7169203331",
		ParentSpanID: "00f067aa0ba902b7",
		Name:         "dataverwerking.bronbevraging",
		StartTime:    start,
		EndTime:      start.Add(1500 * time.Millisecond),
		Status:       StatusOK,
		Resource:     map[string]any{"service.name": "graphql-server"},
		Attributes:   map[string]any{AttrDataSubjectID: "PI-abc123"},
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// §3.2.2.5–6: uint64 milliseconds since the epoch, not RFC 3339.
	if got, want := raw["start_time"], float64(1788343200000); got != want {
		t.Errorf("start_time = %v, want %v milliseconds since the epoch", got, want)
	}
	if got, want := raw["end_time"], float64(1788343201500); got != want {
		t.Errorf("end_time = %v, want %v", got, want)
	}

	// §3.2.2.8: resource is an object with an attributes field, not a map of
	// attributes directly.
	resource, ok := raw["resource"].(map[string]any)
	if !ok {
		t.Fatalf("resource = %T, want an object", raw["resource"])
	}
	attributes, ok := resource["attributes"].(map[string]any)
	if !ok {
		t.Fatalf("resource.attributes = %T, want an object", resource["attributes"])
	}
	if attributes["service.name"] != "graphql-server" {
		t.Errorf("resource.attributes = %#v", attributes)
	}

	// Field names are snake_case throughout.
	for _, field := range []string{"trace_id", "span_id", "parent_span_id", "name", "status", "attributes"} {
		if _, present := raw[field]; !present {
			t.Errorf("field %q missing: %s", field, encoded)
		}
	}
	if len(raw) != 9 {
		t.Errorf("record carries %d fields, want exactly the nine the standard defines: %s", len(raw), encoded)
	}
}

// A record with no resource omits it rather than sending an empty object.
func TestRecordWithoutAResourceOmitsIt(t *testing.T) {
	encoded, err := json.Marshal(Record{Name: "x", Attributes: map[string]any{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	_ = json.Unmarshal(encoded, &raw)
	if _, present := raw["resource"]; present {
		t.Errorf("resource should be omitted when empty: %s", encoded)
	}
	// attributes is mandatory, so it is always present even when empty.
	if _, present := raw["attributes"]; !present {
		t.Errorf("attributes is mandatory: %s", encoded)
	}
}

func TestRecordRoundTripsThroughTheWireFormat(t *testing.T) {
	start := time.Date(2026, 9, 2, 10, 0, 0, 123_000_000, time.UTC)
	original := Record{
		TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331",
		Name: "dataverwerking.test", StartTime: start, EndTime: start.Add(time.Second),
		Status: StatusOK, Resource: map[string]any{"service.name": "s"},
		Attributes: map[string]any{"gbo.year": float64(2025)},
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded Record
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !decoded.StartTime.Equal(original.StartTime) || !decoded.EndTime.Equal(original.EndTime) {
		t.Errorf("times did not round-trip: %v..%v", decoded.StartTime, decoded.EndTime)
	}
	if decoded.Resource["service.name"] != "s" || decoded.Attributes["gbo.year"] != float64(2025) {
		t.Errorf("maps did not round-trip: %#v %#v", decoded.Resource, decoded.Attributes)
	}
}

// Milliseconds are the unit, so sub-millisecond precision is lost by design.
// Stating it in a test stops someone reintroducing nanoseconds later.
func TestSubMillisecondPrecisionIsNotPreserved(t *testing.T) {
	start := time.Date(2026, 9, 2, 10, 0, 0, 999_999, time.UTC) // 0.999999 ms
	if got := EpochMillis(start); got != EpochMillis(start.Truncate(time.Millisecond)) {
		t.Errorf("EpochMillis should truncate to whole milliseconds, got %d", got)
	}
}

// A time before the epoch would wrap to an enormous uint64 rather than fail.
func TestTimesBeforeTheEpochClampRatherThanWrap(t *testing.T) {
	if got := EpochMillis(time.Date(1969, 1, 1, 0, 0, 0, 0, time.UTC)); got != 0 {
		t.Errorf("EpochMillis = %d, want 0 rather than a wrapped value", got)
	}
}
