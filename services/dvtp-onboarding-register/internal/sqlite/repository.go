// Package sqlite implements onboarding persistence with SQLite.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dvtp-onboarding-register/internal/onboarding"

	_ "modernc.org/sqlite"
)

type Repository struct {
	db *sql.DB
}

// The storage column keeps its original name so existing demo volumes can be
// migrated in place; the domain and JSON API expose allowed_source_oins.
const schema = `
CREATE TABLE IF NOT EXISTS participants (
    oin TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    allowed_sources TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (length(oin) = 20 AND oin NOT GLOB '*[^0-9]*')
);
`

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
	for _, pragma := range []string{"PRAGMA busy_timeout = 5000", "PRAGMA journal_mode = WAL", "PRAGMA foreign_keys = ON"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure sqlite: %w", err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize sqlite schema: %w", err)
	}
	if _, err := db.Exec(`
        UPDATE participants
        SET allowed_sources = replace(
            replace(allowed_sources, '"belastingdienst"', '"99999999900000000200"'),
            '"brp"', '"99999999900000000400"'
        )
        WHERE allowed_sources LIKE '%"belastingdienst"%'
           OR allowed_sources LIKE '%"brp"%'
    `); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate source keys to OINs: %w", err)
	}
	return &Repository{db: db}, nil
}

func (r *Repository) Close() error {
	return r.db.Close()
}

func (r *Repository) Save(ctx context.Context, participant onboarding.Participant) error {
	encodedSources, err := json.Marshal(participant.AllowedSourceOINs)
	if err != nil {
		return fmt.Errorf("encode allowed sources: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
        INSERT INTO participants (oin, name, active, allowed_sources, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(oin) DO UPDATE SET
            name = excluded.name,
            active = excluded.active,
            allowed_sources = excluded.allowed_sources,
            updated_at = excluded.updated_at
    `, participant.OIN, participant.Name, participant.Active, string(encodedSources), now())
	if err != nil {
		return fmt.Errorf("save participant: %w", err)
	}
	return nil
}

func (r *Repository) InsertIfAbsent(ctx context.Context, participant onboarding.Participant) error {
	encodedSources, err := json.Marshal(participant.AllowedSourceOINs)
	if err != nil {
		return fmt.Errorf("encode allowed sources: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `
        INSERT OR IGNORE INTO participants (oin, name, active, allowed_sources, updated_at)
        VALUES (?, ?, ?, ?, ?)
    `, participant.OIN, participant.Name, participant.Active, string(encodedSources), now()); err != nil {
		return fmt.Errorf("insert participant: %w", err)
	}
	return nil
}

func (r *Repository) List(ctx context.Context) ([]onboarding.Participant, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT oin, name, active, allowed_sources, updated_at
        FROM participants
        ORDER BY name COLLATE NOCASE, oin
    `)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	participants := make([]onboarding.Participant, 0)
	for rows.Next() {
		var participant onboarding.Participant
		var encodedSources string
		if err := rows.Scan(&participant.OIN, &participant.Name, &participant.Active, &encodedSources, &participant.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan participant: %w", err)
		}
		if err := json.Unmarshal([]byte(encodedSources), &participant.AllowedSourceOINs); err != nil {
			return nil, fmt.Errorf("decode sources for %s: %w", participant.OIN, err)
		}
		participants = append(participants, participant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate participants: %w", err)
	}
	return participants, nil
}

func (r *Repository) ToggleActive(ctx context.Context, oin string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
        UPDATE participants
        SET active = CASE active WHEN 1 THEN 0 ELSE 1 END,
            updated_at = ?
        WHERE oin = ?
    `, now(), oin)
	if err != nil {
		return false, fmt.Errorf("toggle participant: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read affected rows: %w", err)
	}
	return count == 1, nil
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
