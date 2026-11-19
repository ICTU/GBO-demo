package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"ldv-logboek/internal/ldv"
)

func sampleRecord(spanID string) ldv.Stored {
	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	return ldv.Stored{
		Record: ldv.Record{
			TraceID:   "0af7651916cd43dd8448eb211c80319c",
			SpanID:    spanID,
			Name:      "bronquery.doorgifte",
			Status:    ldv.StatusOK,
			StartTime: start,
			EndTime:   start.Add(12 * time.Millisecond),
			Resource:  map[string]any{"service.name": "bron-sidecar"},
			Attributes: map[string]any{
				ldv.AttrProcessingActivityID: "bd-ib-2025@v1",
				ldv.AttrDataSubjectID:        "LP-abc123",
				ldv.AttrDataSubjectIDType:    "logboek-pseudoniem",
				"gbo.belastingjaren":         []any{float64(2025)},
			},
		},
		ReceivedAt: start.Add(20 * time.Millisecond),
	}
}

func TestAppendReportsADuplicateSpan(t *testing.T) {
	repository, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = repository.Close() }()

	ctx := context.Background()
	if err := repository.Append(ctx, sampleRecord("b7ad6b7169203331")); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := repository.Append(ctx, sampleRecord("b7ad6b7169203331")); !errors.Is(err, ldv.ErrDuplicateRecord) {
		t.Fatalf("expected ErrDuplicateRecord, got %v", err)
	}
	// A different span under the same trace is a different Dataverwerking.
	if err := repository.Append(ctx, sampleRecord("00f067aa0ba902b7")); err != nil {
		t.Fatalf("sibling span rejected: %v", err)
	}
	count, err := repository.Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

// LDV requires a confirmed write to be durable. This is the property the
// producer relies on when it treats the confirmation as evidence, so it is
// asserted against a real file rather than an in-memory database.
func TestRecordsSurviveAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logboek.db")
	ctx := context.Background()

	first, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Append(ctx, sampleRecord("b7ad6b7169203331")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()

	count, err := second.Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("count after reopen = %d, want 1", count)
	}
}

func TestOpenCreatesTheDatabaseDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "logboek.db")
	repository, err := Open(path)
	if err != nil {
		t.Fatalf("open into a missing directory: %v", err)
	}
	_ = repository.Close()
}

// Records whose start times differ only in trailing fraction zeros: as
// RFC3339Nano text "05.5Z" sorts before "05Z" and ".12Z" before ".1Z".
func TestQueryOrdersAndFiltersByTimeNotText(t *testing.T) {
	repository, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = repository.Close() }()

	ctx := context.Background()
	second := time.Date(2026, 9, 2, 10, 0, 5, 0, time.UTC)
	starts := map[string]time.Duration{
		"0000000000000001": 500 * time.Millisecond, // 05.5Z
		"0000000000000002": 0,                      // 05Z
		"0000000000000003": 120 * time.Millisecond, // 05.12Z
		"0000000000000004": 100 * time.Millisecond, // 05.1Z
	}
	for spanID, offset := range starts {
		record := sampleRecord(spanID)
		record.StartTime = second.Add(offset)
		record.EndTime = record.StartTime
		if err := repository.Append(ctx, record); err != nil {
			t.Fatalf("append %s: %v", spanID, err)
		}
	}

	spanIDs := func(query ldv.Query) []string {
		t.Helper()
		query.TraceID = sampleRecord("").TraceID
		query.Limit = ldv.MaxReadLimit
		records, err := repository.Query(ctx, query)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		ids := make([]string, 0, len(records))
		for _, record := range records {
			ids = append(ids, record.SpanID[len(record.SpanID)-1:])
		}
		return ids
	}
	at := func(offset time.Duration) *time.Time {
		value := second.Add(offset)
		return &value
	}

	for _, tc := range []struct {
		name  string
		query ldv.Query
		want  string
	}{
		{"order", ldv.Query{}, "2431"},
		{"start at the whole second includes all", ldv.Query{StartTime: at(0)}, "2431"},
		{"start at .1 excludes 05Z", ldv.Query{StartTime: at(100 * time.Millisecond)}, "431"},
		{"start at .12 excludes .1", ldv.Query{StartTime: at(120 * time.Millisecond)}, "31"},
		{"end at the whole second keeps only 05Z", ldv.Query{EndTime: at(0)}, "2"},
		{"end at .1 excludes .12 and .5", ldv.Query{EndTime: at(100 * time.Millisecond)}, "24"},
		{"end at .12 excludes .5", ldv.Query{EndTime: at(120 * time.Millisecond)}, "243"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ""
			for _, id := range spanIDs(tc.query) {
				got += id
			}
			if got != tc.want {
				t.Fatalf("span order = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseTimeReadsTrimmedFractions(t *testing.T) {
	want := time.Date(2026, 9, 2, 10, 0, 5, 500_000_000, time.UTC)
	for _, text := range []string{"2026-09-02T10:00:05.5Z", formatTime(want)} {
		got, err := parseTime(text)
		if err != nil || !got.Equal(want) {
			t.Fatalf("parseTime(%q) = %v, %v; want %v", text, got, err, want)
		}
	}
	if got := formatTime(want); got != "2026-09-02T10:00:05.500000000Z" {
		t.Fatalf("formatTime = %q", got)
	}
}
