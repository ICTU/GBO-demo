// Package sqlite stores LDV records durably.
//
// SQLite, like the dvtp-onboarding-register: the demo needs a store that
// survives a restart and can be inspected with one command, not a cluster.
// What matters for LDV is the write discipline — every Append commits before
// it returns, and there is no batching layer anywhere in this package.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ldv-logboek/internal/ldv"

	_ "modernc.org/sqlite"
)

type Repository struct {
	db *sql.DB
}

// The record columns are the LDV fields a reader queries on; resource and
// attributes stay JSON because they are open maps. The primary key is
// (trace_id, span_id) — the identity of the operation — which is what makes a
// producer's retry idempotent rather than duplicating a Dataverwerking.
const schema = `
CREATE TABLE IF NOT EXISTS records (
    trace_id TEXT NOT NULL,
    span_id TEXT NOT NULL,
    parent_span_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    start_time TEXT NOT NULL,
    end_time TEXT NOT NULL,
    received_at TEXT NOT NULL,
    processing_activity_id TEXT NOT NULL,
    data_subject_id TEXT NOT NULL,
    data_subject_id_type TEXT NOT NULL,
    resource TEXT NOT NULL,
    attributes TEXT NOT NULL,
    PRIMARY KEY (trace_id, span_id)
);
`

// Open prepares the store. It creates the containing directory so a fresh
// volume needs no init container.
func Open(path string) (*Repository, error) {
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
	// synchronous=FULL rather than the WAL default of NORMAL: a confirmed LDV
	// write must survive an unclean shutdown, and "probably flushed" is not
	// what the producer was told.
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
	return &Repository{db: db}, nil
}

func (r *Repository) Close() error { return r.db.Close() }

// Append writes one record, synchronously. A primary-key collision is
// reported as ldv.ErrDuplicateRecord so the core can distinguish a replay
// from a storage failure.
func (r *Repository) Append(ctx context.Context, stored ldv.Stored) error {
	resource, err := json.Marshal(orEmptyMap(stored.Resource))
	if err != nil {
		return fmt.Errorf("encode resource: %w", err)
	}
	attributes, err := json.Marshal(stored.Attributes)
	if err != nil {
		return fmt.Errorf("encode attributes: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
        INSERT INTO records (
            trace_id, span_id, parent_span_id, name, status,
            start_time, end_time, received_at,
            processing_activity_id, data_subject_id, data_subject_id_type,
            resource, attributes
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		stored.TraceID, stored.SpanID, stored.ParentSpanID, stored.Name, stored.Status,
		formatTime(stored.StartTime), formatTime(stored.EndTime), formatTime(stored.ReceivedAt),
		stored.ProcessingActivityID(), stored.DataSubjectID(), stored.DataSubjectIDType(),
		string(resource), string(attributes),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ldv.ErrDuplicateRecord
		}
		return fmt.Errorf("insert record: %w", err)
	}
	return nil
}

// Count reports how many records are stored. Used by the health endpoint and
// by the restart-survival test.
func (r *Repository) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM records`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count records: %w", err)
	}
	return count, nil
}

func orEmptyMap(resource map[string]string) map[string]string {
	if resource == nil {
		return map[string]string{}
	}
	return resource
}

// formatTime stores times as RFC 3339 with nanoseconds in UTC, so the text
// ordering of the column equals the chronological ordering.
func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

// isUniqueViolation recognises the driver's constraint error. modernc's
// sqlite reports it as a message rather than a typed error, so this matches on
// the text; a false negative would surface as a 500 rather than as silent
// data loss.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var target interface{ Error() string }
	if !errors.As(err, &target) {
		return false
	}
	message := strings.ToUpper(target.Error())
	return strings.Contains(message, "UNIQUE CONSTRAINT FAILED") || strings.Contains(message, "SQLITE_CONSTRAINT_PRIMARYKEY")
}
