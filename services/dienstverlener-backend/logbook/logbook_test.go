package logbook

import (
	"context"
	"reflect"
	"testing"
	"time"

	"gbo-demo/dienstverlener-backend/consumer"
	ldv "gbo-demo/ldv-client"
	"gbo-demo/ldv-client/ldvtest"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const bdLogbook = "https://logboek.belastingdienst.nl/data-processing-operations"

func processing() consumer.Processing {
	start := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	return consumer.Processing{
		At:       consumer.Position{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7"},
		Activity: consumer.Kinds["bd"].Activity,
		Name:     consumer.Kinds["bd"].RecordName,
		Subject:  "PI-abc123",
		Scope:    "bd:ib:2025",
		Start:    start,
		End:      start.Add(time.Second),
	}
}

// The consumer's record is the root of the chain, about the PI from the
// consent, with a pointer to where the source logs its half.
func TestARecordPointsToTheSourcesLogbook(t *testing.T) {
	fake := ldvtest.New(t, consumer.Kinds["bd"].Activity)
	l := &Logbook{Client: fake.Client(t, "dienstverlener-backend"), NextLogbooks: map[string]string{"bd": bdLogbook}}

	if err := l.Record(context.Background(), processing()); err != nil {
		t.Fatalf("Record: %v", err)
	}

	records := fake.Written()
	if len(records) != 1 {
		t.Fatalf("wrote %d records, want 1", len(records))
	}
	record := records[0]
	if record.TraceID != processing().At.TraceID || record.SpanID != processing().At.SpanID {
		t.Errorf("record at %s/%s, want the processing's position", record.TraceID, record.SpanID)
	}
	if record.ParentSpanID != "" {
		t.Errorf("parent_span_id = %q; the consumer starts the chain", record.ParentSpanID)
	}
	if got := record.Attributes[ldv.AttrProcessingActivityID]; got != consumer.Kinds["bd"].Activity {
		t.Errorf("processing_activity_id = %v", got)
	}
	if got := record.Attributes[ldv.AttrNextLogbookID]; got != bdLogbook {
		t.Errorf("nextLogbookId = %v, want the source's read API", got)
	}
	if got := record.Attributes[ldv.AttrDataSubjectID]; got != "PI-abc123" {
		t.Errorf("data_subject_id = %v, want the PI", got)
	}
	if record.Status != ldv.StatusOK {
		t.Errorf("status = %q, want OK", record.Status)
	}
}

func TestAFailedCallIsRecordedAsAnError(t *testing.T) {
	fake := ldvtest.New(t, consumer.Kinds["bd"].Activity)
	l := &Logbook{Client: fake.Client(t, "dienstverlener-backend")}
	p := processing()
	p.Failed = true

	if err := l.Record(context.Background(), p); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if records := fake.Written(); len(records) != 1 || records[0].Status != ldv.StatusError {
		t.Fatalf("records = %+v, want one with status ERROR", records)
	}
}

// A record the logbook refuses is an error, so the core can withhold the
// answer.
func TestARefusedRecordIsAnError(t *testing.T) {
	fake := ldvtest.New(t, consumer.Kinds["bd"].Activity)
	fake.RefuseEverything()
	l := &Logbook{Client: fake.Client(t, "dienstverlener-backend")}

	if err := l.Record(context.Background(), processing()); err == nil {
		t.Fatal("a refused record returned nil")
	}
}

// The record lives in the request's own trace.
func TestBeginUsesTheRequestsTrace(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "dvtp.query")
	defer span.End()

	at := (&Logbook{}).Begin(ctx)

	if at.TraceID != span.SpanContext().TraceID().String() {
		t.Errorf("trace = %q, want the request's %s", at.TraceID, span.SpanContext().TraceID())
	}
	if !ldv.IsSpanID(at.SpanID) || at.SpanID == span.SpanContext().SpanID().String() {
		t.Errorf("span = %q, want a fresh span id of its own", at.SpanID)
	}
}

func TestParseNextLogbooks(t *testing.T) {
	got := ParseNextLogbooks(" bd = https://a , malformed, =https://b, lvg=https://c,")
	want := map[string]string{"bd": "https://a", "lvg": "https://c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseNextLogbooks = %v, want %v", got, want)
	}
}
