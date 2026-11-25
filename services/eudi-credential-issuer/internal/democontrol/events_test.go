package democontrol_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/democontrol"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

// clock is a time a test moves on.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// timeline returns the events GET /demo/events shows.
func timeline(t *testing.T, h http.Handler) []issuance.Event {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/events", nil))
	var body struct {
		Events []issuance.Event `json:"events"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Events == nil {
		t.Fatalf("GET /demo/events = %d %s", rec.Code, rec.Body.String())
	}
	return body.Events
}

// C4: the timeline shows the events of the last 50 minutes, oldest first,
// and forgets older ones.
func testRetention(t *testing.T, store democontrol.EventStore) {
	c := &clock{now: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)}
	log := democontrol.NewEventLog(store, c.Now, slog.Default())
	h := democontrol.Handler(newOverrides(), log)
	ctx := context.Background()

	if got := timeline(t, h); len(got) != 0 {
		t.Fatalf("empty timeline = %+v", got)
	}
	log.Record(ctx, issuance.Event{At: c.Now(), Kind: issuance.EventSessionStarted, Session: "first"})
	c.advance(30 * time.Minute)
	log.Record(ctx, issuance.Event{At: c.Now(), Kind: issuance.EventTokenIssued, Session: "first"})
	got := timeline(t, h)
	if len(got) != 2 || got[0].Kind != issuance.EventSessionStarted || got[1].Kind != issuance.EventTokenIssued {
		t.Fatalf("after 30 minutes = %+v, want both events, oldest first", got)
	}
	c.advance(25 * time.Minute)
	if got := timeline(t, h); len(got) != 1 || got[0].Kind != issuance.EventTokenIssued {
		t.Fatalf("after 55 minutes = %+v, want only the event of 25 minutes ago", got)
	}
	log.Record(ctx, issuance.Event{At: c.Now(), Kind: issuance.EventCredentialIssued, Session: "first", Credentials: 3})
	if got := timeline(t, h); len(got) != 2 || got[1].Credentials != 3 {
		t.Fatalf("after a new event = %+v", got)
	}
}

func TestRetentionInMemory(t *testing.T) {
	testRetention(t, &democontrol.MemoryEvents{})
}

// The PostgreSQL store, against ISSUER_TEST_DATABASE_URL as the issuer's
// own store tests.
func TestRetentionInPostgres(t *testing.T) {
	url := os.Getenv("ISSUER_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("ISSUER_TEST_DATABASE_URL is not set")
	}
	schema := "timeline_test_" + time.Now().Format("150405")
	store, err := democontrol.OpenPostgresEvents(context.Background(), url, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DropSchema(context.Background()); store.Close() })
	testRetention(t, store)
}
