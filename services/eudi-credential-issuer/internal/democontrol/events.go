package democontrol

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

// Retention is how long the timeline keeps an event.
const Retention = 50 * time.Minute

// maxEvents bounds what the timeline returns at once.
const maxEvents = 1000

// EventStore keeps events for the timeline.
type EventStore interface {
	Add(ctx context.Context, e issuance.Event) error
	// Since returns the events at or after from, oldest first, at most
	// limit of the newest.
	Since(ctx context.Context, from time.Time, limit int) ([]issuance.Event, error)
	// Purge removes the events before cutoff.
	Purge(ctx context.Context, cutoff time.Time) error
}

// EventLog is the issuance.Events port of the demo: it keeps the events of
// the last Retention for the developer portal's timeline.
type EventLog struct {
	store EventStore
	now   func() time.Time
	log   *slog.Logger
}

// NewEventLog returns an event log over store.
func NewEventLog(store EventStore, now func() time.Time, log *slog.Logger) *EventLog {
	return &EventLog{store: store, now: now, log: log}
}

// Record implements issuance.Events. A failure is logged: the timeline
// misses the event, the issuance goes on.
func (l *EventLog) Record(ctx context.Context, e issuance.Event) {
	ctx = context.WithoutCancel(ctx)
	if err := l.store.Add(ctx, e); err != nil {
		l.log.Warn("timeline event not recorded", "kind", e.Kind, "error", err)
	}
	if err := l.store.Purge(ctx, l.now().Add(-Retention)); err != nil {
		l.log.Warn("timeline events not purged", "error", err)
	}
}

// Events returns the events of the last Retention, oldest first.
func (l *EventLog) Events(ctx context.Context) ([]issuance.Event, error) {
	return l.store.Since(ctx, l.now().Add(-Retention), maxEvents)
}

// MemoryEvents keeps events in memory, for ISSUER_STORE=memory and tests.
type MemoryEvents struct {
	mu     sync.Mutex
	events []issuance.Event
}

func (m *MemoryEvents) Add(_ context.Context, e issuance.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func (m *MemoryEvents) Since(_ context.Context, from time.Time, limit int) ([]issuance.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []issuance.Event
	for _, e := range m.events {
		if !e.At.Before(from) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (m *MemoryEvents) Purge(_ context.Context, cutoff time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.events[:0]
	for _, e := range m.events {
		if !e.At.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	m.events = kept
	return nil
}

var schemaName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// PostgresEvents keeps events in a schema of their own, apart from the
// issuer's tables, so the issuer's schema stays free of demo tables.
type PostgresEvents struct {
	pool   *pgxpool.Pool
	schema string
}

// OpenPostgresEvents connects to databaseURL and creates schema and its
// table when they do not exist.
func OpenPostgresEvents(ctx context.Context, databaseURL, schema string) (*PostgresEvents, error) {
	if !schemaName.MatchString(schema) {
		return nil, fmt.Errorf("schema %q is invalid", schema)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	p := &PostgresEvents{pool: pool, schema: schema}
	for _, stmt := range []string{
		`CREATE SCHEMA IF NOT EXISTS ` + schema,
		`CREATE TABLE IF NOT EXISTS ` + schema + `.timeline_events (id BIGSERIAL PRIMARY KEY, at TIMESTAMPTZ NOT NULL, event JSONB NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS timeline_events_at ON ` + schema + `.timeline_events (at)`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			pool.Close()
			return nil, fmt.Errorf("prepare timeline events: %w", err)
		}
	}
	return p, nil
}

func (p *PostgresEvents) Close() { p.pool.Close() }

func (p *PostgresEvents) Add(ctx context.Context, e issuance.Event) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `INSERT INTO `+p.schema+`.timeline_events (at, event) VALUES ($1, $2)`, e.At, raw)
	return err
}

func (p *PostgresEvents) Since(ctx context.Context, from time.Time, limit int) ([]issuance.Event, error) {
	rows, err := p.pool.Query(ctx, `SELECT event FROM (SELECT id, at, event FROM `+p.schema+`.timeline_events
		WHERE at >= $1 ORDER BY at DESC, id DESC LIMIT $2) newest ORDER BY at, id`, from, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []issuance.Event
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var e issuance.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *PostgresEvents) Purge(ctx context.Context, cutoff time.Time) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM `+p.schema+`.timeline_events WHERE at < $1`, cutoff)
	return err
}
