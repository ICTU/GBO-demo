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
			Resource:  map[string]string{"service.name": "bron-sidecar"},
			Attributes: map[string]any{
				ldv.AttrProcessingActivityID: "bd-ib-2025@v1",
				ldv.AttrDataSubjectID:        "PI-abc123",
				ldv.AttrDataSubjectIDType:    "pi",
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
