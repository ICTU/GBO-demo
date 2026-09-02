package ldv

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeRepository is the in-memory stand-in for the storage port, so the use
// case can be tested at the core boundary without SQLite.
type fakeRepository struct {
	stored  []Stored
	failure error
}

// Query is the read half of the port. The fake filters on trace id only —
// enough for the core's read rules, which are about validation and capping
// rather than about SQL.
func (f *fakeRepository) Query(_ context.Context, query Query) ([]Stored, error) {
	if f.failure != nil {
		return nil, f.failure
	}
	var matched []Stored
	for _, stored := range f.stored {
		if query.TraceID != "" && stored.TraceID != query.TraceID {
			continue
		}
		if len(matched) == query.Limit {
			break
		}
		matched = append(matched, stored)
	}
	return matched, nil
}

func (f *fakeRepository) Append(_ context.Context, stored Stored) error {
	if f.failure != nil {
		return f.failure
	}
	for _, existing := range f.stored {
		if existing.TraceID == stored.TraceID && existing.SpanID == stored.SpanID {
			return ErrDuplicateRecord
		}
	}
	f.stored = append(f.stored, stored)
	return nil
}

func testRegister(t *testing.T) *Register {
	t.Helper()
	register := &Register{
		Verantwoordelijke: "Belastingdienst",
		Disclaimer:        "demo",
		Activities: []Activity{
			{ID: "bd-ib-2025", Version: "v1", Name: "Verstrekken IB 2025", Doel: "demo"},
		},
	}
	if err := register.index(); err != nil {
		t.Fatalf("index test register: %v", err)
	}
	return register
}

func newTestLogbook(t *testing.T, repository Repository) *Logbook {
	t.Helper()
	fixed := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	logbook, err := NewLogbook(repository, testRegister(t), func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("wire logbook: %v", err)
	}
	return logbook
}

func TestWriteStoresAndConfirms(t *testing.T) {
	repository := &fakeRepository{}
	logbook := newTestLogbook(t, repository)

	confirmation, err := logbook.Write(context.Background(), validRecord())
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if confirmation.Duplicate {
		t.Fatal("a first write is not a duplicate")
	}
	if confirmation.ReceivedAt.IsZero() {
		t.Fatal("the confirmation must carry the logbook's receipt time")
	}
	if len(repository.stored) != 1 {
		t.Fatalf("stored %d records, want 1", len(repository.stored))
	}
	// The confirmation is only meaningful if it follows the write; a record
	// the repository never saw must never be confirmed.
	if repository.stored[0].SpanID != confirmation.SpanID {
		t.Fatal("the confirmation does not describe the stored record")
	}
}

// A producer that retries after a timeout must not turn one Dataverwerking
// into two, and must be able to tell that is what happened.
func TestWriteIsIdempotentPerSpan(t *testing.T) {
	repository := &fakeRepository{}
	logbook := newTestLogbook(t, repository)

	if _, err := logbook.Write(context.Background(), validRecord()); err != nil {
		t.Fatalf("first write: %v", err)
	}
	confirmation, err := logbook.Write(context.Background(), validRecord())
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !confirmation.Duplicate {
		t.Fatal("a replay must be reported as a duplicate")
	}
	if len(repository.stored) != 1 {
		t.Fatalf("stored %d records, want 1", len(repository.stored))
	}
}

func TestWriteRejectsAnUnknownProcessingActivity(t *testing.T) {
	repository := &fakeRepository{}
	logbook := newTestLogbook(t, repository)

	record := validRecord()
	record.Attributes[AttrProcessingActivityID] = "bd-ib-2019@v1"
	if _, err := logbook.Write(context.Background(), record); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("expected ErrInvalidRecord, got %v", err)
	}
	if len(repository.stored) != 0 {
		t.Fatal("an invalid record must not reach storage")
	}
}

// The confirmation is the producer's evidence that the record is durable, so
// a storage failure must surface as an error and never as a confirmation.
func TestWriteDoesNotConfirmWhenStorageFails(t *testing.T) {
	repository := &fakeRepository{failure: errors.New("disk on fire")}
	logbook := newTestLogbook(t, repository)

	if _, err := logbook.Write(context.Background(), validRecord()); err == nil {
		t.Fatal("a failed write must not be confirmed")
	}
}

func TestNewLogbookRequiresItsCollaborators(t *testing.T) {
	if _, err := NewLogbook(nil, testRegister(t), nil); err == nil {
		t.Error("a logbook without a repository must not start")
	}
	if _, err := NewLogbook(&fakeRepository{}, nil, nil); err == nil {
		t.Error("a logbook without a register must not start")
	}
}

// A read has to name one of the three axes. Without that a logbook becomes
// browsable, which is the opposite of what it is for.
func TestReadRequiresASelector(t *testing.T) {
	logbook := newTestLogbook(t, &fakeRepository{})

	if _, err := logbook.Read(context.Background(), Query{}); !errors.Is(err, ErrNoSelector) {
		t.Fatalf("expected ErrNoSelector, got %v", err)
	}
}

func TestReadReturnsTheRecordsOfOneTrace(t *testing.T) {
	repository := &fakeRepository{}
	logbook := newTestLogbook(t, repository)
	ctx := context.Background()

	first := validRecord()
	second := validRecord()
	second.SpanID = "00f067aa0ba902b7"
	other := validRecord()
	other.TraceID = "11111111111111111111111111111111"
	for _, record := range []Record{first, second, other} {
		if _, err := logbook.Write(ctx, record); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	records, err := logbook.Read(ctx, Query{TraceID: first.TraceID})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("read %d records, want the two of that trace", len(records))
	}
}

// The cap is what keeps a read from becoming a dump of the whole logbook.
func TestReadCapsTheResult(t *testing.T) {
	repository := &fakeRepository{}
	logbook := newTestLogbook(t, repository)
	ctx := context.Background()

	for index := 0; index < 5; index++ {
		record := validRecord()
		record.SpanID = fmt.Sprintf("%016x", index)
		if _, err := logbook.Write(ctx, record); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	records, err := logbook.Read(ctx, Query{TraceID: validRecord().TraceID, Limit: 2})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("read %d records, want the requested 2", len(records))
	}
	// An absurd limit is capped rather than honoured.
	capped, err := Query{TraceID: "x", Limit: 10_000}.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if capped.Limit != MaxReadLimit {
		t.Fatalf("limit = %d, want the cap %d", capped.Limit, MaxReadLimit)
	}
}
