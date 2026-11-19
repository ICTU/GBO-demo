// Package sqlite stores registrations durably, in SQLite like the logbooks
// and the onboarding register: the demo needs a store that survives a restart
// and can be read with one command.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"consent-usage-index/internal/usage"

	_ "modernc.org/sqlite"
)

// timeLayout is fixed width, so ordering the text column orders the times.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// The primary key is the identity of a registration (#687): one source's
// record of one trace under one legal basis. It is what makes a retry return
// the stored registration instead of adding a second one.
const schema = `
CREATE TABLE IF NOT EXISTS registrations (
    logbook_id TEXT NOT NULL,
    trace_id TEXT NOT NULL,
    grondslag_type TEXT NOT NULL,
    grondslag_ref TEXT NOT NULL,
    registered_at TEXT NOT NULL,
    PRIMARY KEY (logbook_id, trace_id, grondslag_type, grondslag_ref)
);
CREATE INDEX IF NOT EXISTS registrations_by_grondslag
    ON registrations (grondslag_type, grondslag_ref, registered_at);
`

// Store implements usage.Store.
type Store struct {
	db *sql.DB
}

// Open prepares the store and creates its directory, so a fresh volume needs
// no init container.
func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	// synchronous=FULL: a source withholds its data until the index confirms,
	// so a confirmed registration has to survive an unclean shutdown.
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = FULL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize sqlite schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping reports whether the database answers, for the health check.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Add implements usage.Store.
func (s *Store) Add(ctx context.Context, r usage.Registration) (usage.Registration, bool, error) {
	result, err := s.db.ExecContext(ctx, `
        INSERT INTO registrations (logbook_id, trace_id, grondslag_type, grondslag_ref, registered_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT (logbook_id, trace_id, grondslag_type, grondslag_ref) DO NOTHING`,
		r.LogbookID, r.TraceID, r.GrondslagType, r.GrondslagRef, r.RegisteredAt.UTC().Format(timeLayout))
	if err != nil {
		return usage.Registration{}, false, fmt.Errorf("insert registration: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return usage.Registration{}, false, fmt.Errorf("insert registration: %w", err)
	}
	if affected == 1 {
		return r, true, nil
	}
	var registeredAt string
	err = s.db.QueryRowContext(ctx, `
        SELECT registered_at FROM registrations
        WHERE logbook_id = ? AND trace_id = ? AND grondslag_type = ? AND grondslag_ref = ?`,
		r.LogbookID, r.TraceID, r.GrondslagType, r.GrondslagRef).Scan(&registeredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return usage.Registration{}, false, errors.New("registration conflicted but is not stored")
	}
	if err != nil {
		return usage.Registration{}, false, fmt.Errorf("read existing registration: %w", err)
	}
	r.RegisteredAt, err = time.Parse(timeLayout, registeredAt)
	if err != nil {
		return usage.Registration{}, false, fmt.Errorf("parse registered_at: %w", err)
	}
	return r, false, nil
}

// ByGrondslag implements usage.Store.
func (s *Store) ByGrondslag(ctx context.Context, grondslagType, grondslagRef string) ([]usage.Registration, error) {
	rows, err := s.db.QueryContext(ctx, `
        SELECT logbook_id, trace_id, registered_at FROM registrations
        WHERE grondslag_type = ? AND grondslag_ref = ?
        ORDER BY registered_at DESC`, grondslagType, grondslagRef)
	if err != nil {
		return nil, fmt.Errorf("query registrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []usage.Registration
	for rows.Next() {
		r := usage.Registration{GrondslagType: grondslagType, GrondslagRef: grondslagRef}
		var registeredAt string
		if err := rows.Scan(&r.LogbookID, &r.TraceID, &registeredAt); err != nil {
			return nil, fmt.Errorf("scan registration: %w", err)
		}
		if r.RegisteredAt, err = time.Parse(timeLayout, registeredAt); err != nil {
			return nil, fmt.Errorf("parse registered_at: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
