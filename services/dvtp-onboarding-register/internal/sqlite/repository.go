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

const schema = `
CREATE TABLE IF NOT EXISTS participants (
    peer_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    allowed_sources TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (length(peer_id) = 20 AND peer_id NOT GLOB '*[^A-Za-z0-9]*')
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
	if err := migrateNumericOINSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Repository{db: db}, nil
}

func migrateNumericOINSchema(db *sql.DB) error {
	var peerIDColumns int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('participants') WHERE name = 'peer_id'`).Scan(&peerIDColumns); err != nil {
		return fmt.Errorf("inspect participant schema: %w", err)
	}
	if peerIDColumns != 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("start participant schema migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range []string{
		`ALTER TABLE participants RENAME TO participants_numeric_oin`,
		schema,
		`INSERT INTO participants (peer_id, name, active, allowed_sources, updated_at)
         SELECT oin, name, active, allowed_sources, updated_at FROM participants_numeric_oin`,
		`DROP TABLE participants_numeric_oin`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("migrate participant schema to Peer IDs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit participant schema migration: %w", err)
	}
	return nil
}

func (r *Repository) Close() error {
	return r.db.Close()
}

func (r *Repository) Save(ctx context.Context, participant onboarding.Participant) error {
	encodedSources, err := json.Marshal(participant.AllowedSourcePeerIDs)
	if err != nil {
		return fmt.Errorf("encode allowed sources: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
        INSERT INTO participants (peer_id, name, active, allowed_sources, updated_at)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(peer_id) DO UPDATE SET
            name = excluded.name,
            active = excluded.active,
            allowed_sources = excluded.allowed_sources,
            updated_at = excluded.updated_at
    `, participant.PeerID, participant.Name, participant.Active, string(encodedSources), now())
	if err != nil {
		return fmt.Errorf("save participant: %w", err)
	}
	return nil
}

func (r *Repository) InsertIfAbsent(ctx context.Context, participant onboarding.Participant) error {
	encodedSources, err := json.Marshal(participant.AllowedSourcePeerIDs)
	if err != nil {
		return fmt.Errorf("encode allowed sources: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `
        INSERT OR IGNORE INTO participants (peer_id, name, active, allowed_sources, updated_at)
        VALUES (?, ?, ?, ?, ?)
    `, participant.PeerID, participant.Name, participant.Active, string(encodedSources), now()); err != nil {
		return fmt.Errorf("insert participant: %w", err)
	}
	return nil
}

func (r *Repository) UpdateDetails(ctx context.Context, participant onboarding.Participant) (bool, error) {
	encodedSources, err := json.Marshal(participant.AllowedSourcePeerIDs)
	if err != nil {
		return false, fmt.Errorf("encode allowed sources: %w", err)
	}
	result, err := r.db.ExecContext(ctx, `
        UPDATE participants
        SET name = ?, allowed_sources = ?, updated_at = ?
        WHERE peer_id = ?
    `, participant.Name, string(encodedSources), now(), participant.PeerID)
	if err != nil {
		return false, fmt.Errorf("update participant details: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read affected rows: %w", err)
	}
	return count == 1, nil
}

func (r *Repository) List(ctx context.Context) ([]onboarding.Participant, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT peer_id, name, active, allowed_sources, updated_at
        FROM participants
        ORDER BY name COLLATE NOCASE, peer_id
    `)
	if err != nil {
		return nil, fmt.Errorf("list participants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	participants := make([]onboarding.Participant, 0)
	for rows.Next() {
		var participant onboarding.Participant
		var encodedSources string
		if err := rows.Scan(&participant.PeerID, &participant.Name, &participant.Active, &encodedSources, &participant.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan participant: %w", err)
		}
		if err := json.Unmarshal([]byte(encodedSources), &participant.AllowedSourcePeerIDs); err != nil {
			return nil, fmt.Errorf("decode sources for %s: %w", participant.PeerID, err)
		}
		participants = append(participants, participant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate participants: %w", err)
	}
	return participants, nil
}

func (r *Repository) ToggleActive(ctx context.Context, peerID string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `
        UPDATE participants
        SET active = CASE active WHEN 1 THEN 0 ELSE 1 END,
            updated_at = ?
        WHERE peer_id = ?
    `, now(), peerID)
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
