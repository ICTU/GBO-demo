package ldvclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testRecord(spanID string) Record {
	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	return Record{
		TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: spanID,
		Name: "dataverwerking.test", StartTime: start, EndTime: start.Add(time.Millisecond),
		Status: StatusOK, Attributes: map[string]any{AttrDataSubjectID: "PI-abc123"},
	}
}

// collectingLogbook accepts records and can be made to fail, so delivery
// behaviour is testable without timing games.
type collectingLogbook struct {
	mutex    sync.Mutex
	received []Record
	fail     bool
	server   *httptest.Server
}

func newCollectingLogbook(t *testing.T) *collectingLogbook {
	t.Helper()
	logbook := &collectingLogbook{}
	logbook.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logbook.mutex.Lock()
		defer logbook.mutex.Unlock()
		if logbook.fail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var record Record
		if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		logbook.received = append(logbook.received, record)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(logbook.server.Close)
	return logbook
}

func (l *collectingLogbook) count() int {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return len(l.received)
}

func (l *collectingLogbook) setFailing(failing bool) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	l.fail = failing
}

func outboxUnderTest(t *testing.T, logbook *collectingLogbook) (*Outbox, string) {
	t.Helper()
	client, err := New(Config{ServiceName: "test", LogbookURL: logbook.server.URL, WriteToken: "t"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	path := filepath.Join(t.TempDir(), "outbox.jsonl")
	outbox, err := OpenOutbox(path, client)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	t.Cleanup(func() { _ = outbox.Close() })
	client.UseOutbox(outbox)
	return outbox, path
}

// The point of the outbox: a record survives a logbook that is down, and is
// delivered when it comes back. Withholding the response never un-processed
// anything, so not losing the record is what actually matters.
func TestOutboxDeliversWhatWasSpooledWhileTheLogbookWasDown(t *testing.T) {
	logbook := newCollectingLogbook(t)
	outbox, _ := outboxUnderTest(t, logbook)
	logbook.setFailing(true)

	for _, spanID := range []string{"b7ad6b7169203331", "00f067aa0ba902b7"} {
		if err := outbox.Append(testRecord(spanID)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := outbox.Deliver(context.Background()); err == nil {
		t.Fatal("delivery should fail while the logbook is down")
	}
	if logbook.count() != 0 {
		t.Fatalf("logbook received %d records while failing", logbook.count())
	}
	if pending, _ := outbox.Pending(context.Background()); pending != 2 {
		t.Fatalf("pending = %d, want both records still waiting", pending)
	}

	logbook.setFailing(false)
	if err := outbox.Deliver(context.Background()); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if logbook.count() != 2 {
		t.Fatalf("logbook received %d records, want 2", logbook.count())
	}
	if pending, _ := outbox.Pending(context.Background()); pending != 0 {
		t.Fatalf("pending = %d after delivery, want 0", pending)
	}
}

// Delivered records are not sent twice, which is what the cursor is for.
func TestOutboxDoesNotRedeliver(t *testing.T) {
	logbook := newCollectingLogbook(t)
	outbox, _ := outboxUnderTest(t, logbook)

	if err := outbox.Append(testRecord("b7ad6b7169203331")); err != nil {
		t.Fatalf("append: %v", err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := outbox.Deliver(context.Background()); err != nil {
			t.Fatalf("deliver: %v", err)
		}
	}
	if logbook.count() != 1 {
		t.Fatalf("logbook received %d copies, want 1", logbook.count())
	}
}

// The spool is the durability guarantee, so it has to survive a restart —
// which is the case the fsync exists for.
func TestOutboxSurvivesAReopen(t *testing.T) {
	logbook := newCollectingLogbook(t)
	logbook.setFailing(true)
	client, err := New(Config{ServiceName: "test", LogbookURL: logbook.server.URL, WriteToken: "t"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	path := filepath.Join(t.TempDir(), "outbox.jsonl")

	first, err := OpenOutbox(path, client)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.Append(testRecord("b7ad6b7169203331")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := OpenOutbox(path, client)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = second.Close() }()
	if pending, _ := second.Pending(context.Background()); pending != 1 {
		t.Fatalf("pending after reopen = %d, want 1", pending)
	}

	logbook.setFailing(false)
	if err := second.Deliver(context.Background()); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if logbook.count() != 1 {
		t.Fatalf("logbook received %d records after restart, want 1", logbook.count())
	}
}

// Order is preserved and nothing is skipped: delivery stops at the first
// failure rather than racing ahead, because a record filed out of order is a
// tree that cannot be reassembled.
func TestOutboxStopsAtTheFirstFailure(t *testing.T) {
	var delivered []string
	var mutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var record Record
		_ = json.NewDecoder(r.Body).Decode(&record)
		mutex.Lock()
		defer mutex.Unlock()
		if record.SpanID == "00f067aa0ba902b7" {
			http.Error(w, "nope", http.StatusServiceUnavailable)
			return
		}
		delivered = append(delivered, record.SpanID)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client, err := New(Config{ServiceName: "test", LogbookURL: server.URL, WriteToken: "t"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.jsonl"), client)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = outbox.Close() }()

	for _, spanID := range []string{"b7ad6b7169203331", "00f067aa0ba902b7", "1111111111111111"} {
		if err := outbox.Append(testRecord(spanID)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := outbox.Deliver(context.Background()); err == nil {
		t.Fatal("expected delivery to stop at the failing record")
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(delivered) != 1 || delivered[0] != "b7ad6b7169203331" {
		t.Fatalf("delivered = %v; the third record must not overtake the second", delivered)
	}
}

// Through Write, the caller never waits on the logbook.
func TestWriteThroughTheOutboxDoesNotWaitOnTheLogbook(t *testing.T) {
	logbook := newCollectingLogbook(t)
	logbook.setFailing(true)
	client, err := New(Config{ServiceName: "test", LogbookURL: logbook.server.URL, WriteToken: "t"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox.jsonl"), client)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = outbox.Close() }()
	client.UseOutbox(outbox)

	// The logbook is refusing everything, and the write still succeeds:
	// the record is durable, which is what the caller needed.
	if err := client.Write(context.Background(), testRecord("b7ad6b7169203331")); err != nil {
		t.Fatalf("Write through a spool must not depend on the logbook: %v", err)
	}
	if pending, _ := outbox.Pending(context.Background()); pending != 1 {
		t.Fatalf("pending = %d, want the record spooled", pending)
	}
	// And the resource is stamped on the way in, not at delivery.
	records, _, err := outbox.undelivered()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if records[0].Resource["service.name"] != "test" {
		t.Errorf("resource = %#v", records[0].Resource)
	}
}

func TestOpenOutboxCreatesItsDirectory(t *testing.T) {
	client, err := New(Config{ServiceName: "t", LogbookURL: "http://logboek:4016", WriteToken: "t"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "nested", "outbox.jsonl"), client)
	if err != nil {
		t.Fatalf("open into a missing directory: %v", err)
	}
	_ = outbox.Close()
	if _, err := os.Stat(filepath.Join(filepath.Dir(outbox.path))); err != nil {
		t.Errorf("directory not created: %v", err)
	}
}
