package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const consentSchema = `
CREATE TABLE IF NOT EXISTS consents (
    consent_id text PRIMARY KEY,
    status text NOT NULL,
    subject_ref text NOT NULL DEFAULT '',
    dienstverlener_oin text NOT NULL,
    scopes jsonb NOT NULL,
    scope_entries jsonb NOT NULL,
    use_case text NOT NULL,
    created_at timestamptz NOT NULL,
    valid_until timestamptz NOT NULL
);

ALTER TABLE consents ADD COLUMN IF NOT EXISTS subject_ref text NOT NULL DEFAULT '';
UPDATE consents SET subject_ref = '' WHERE subject_ref IS NULL;
ALTER TABLE consents ALTER COLUMN subject_ref SET DEFAULT '';

-- The LDV outbox. It lives in the same database as the consents on purpose:
-- a record and the mutation it describes are written in one transaction, so
-- there is no window in which a consent exists that nothing logged, or a
-- record describes a consent that was never created.
--
-- A file spool cannot give that. It is durable, but it is a second store, and
-- two stores cannot be committed together.
CREATE TABLE IF NOT EXISTS ldv_outbox (
    id bigserial PRIMARY KEY,
    record jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE consents ALTER COLUMN subject_ref SET NOT NULL;
DROP INDEX IF EXISTS consents_pi_status_idx;
ALTER TABLE consents DROP COLUMN IF EXISTS pi;

CREATE INDEX IF NOT EXISTS consents_subject_ref_status_idx
    ON consents (subject_ref, status);
`

type PostgreSQLStore struct {
	pool *pgxpool.Pool
}

type rowScanner interface {
	Scan(dest ...any) error
}

func openConsentStore(ctx context.Context) (ConsentStore, func(), error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return NewStore(), func() {}, nil
	}

	store, err := NewPostgreSQLStore(ctx, databaseURL)
	if err != nil {
		return nil, nil, err
	}

	return store, store.Close, nil
}

func NewPostgreSQLStore(ctx context.Context, databaseURL string) (*PostgreSQLStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()

		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}

	if _, err := pool.Exec(ctx, consentSchema); err != nil {
		pool.Close()

		return nil, fmt.Errorf("initialize PostgreSQL schema: %w", err)
	}

	return &PostgreSQLStore{pool: pool}, nil
}

func (s *PostgreSQLStore) Close() {
	s.pool.Close()
}

// Create stores a consent and its LDV record in one transaction, so neither
// can exist without the other.
func (s *PostgreSQLStore) Create(ctx context.Context, consent *Consent, record []byte) error {
	scopes, err := json.Marshal(consent.Scopes)
	if err != nil {
		return fmt.Errorf("marshal scopes: %w", err)
	}

	scopeEntries, err := json.Marshal(consent.ScopeEntries)
	if err != nil {
		return fmt.Errorf("marshal scope entries: %w", err)
	}

	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	_, err = transaction.Exec(ctx, `
		INSERT INTO consents (
			consent_id,
			status,
			subject_ref,
			dienstverlener_oin,
			scopes,
			scope_entries,
			use_case,
			created_at,
			valid_until
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		consent.ConsentID,
		consent.Status,
		consent.SubjectRef,
		consent.DienstverlenrOIN,
		scopes,
		scopeEntries,
		consent.UseCase,
		consent.CreatedAt,
		consent.ValidUntil,
	)
	if err != nil {
		return fmt.Errorf("insert consent: %w", err)
	}
	if err := appendOutbox(ctx, transaction, record); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit consent and its record: %w", err)
	}
	return nil
}

// appendOutbox writes the LDV record inside whatever transaction it is given.
func appendOutbox(ctx context.Context, executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, record []byte) error {
	if len(record) == 0 {
		return nil
	}
	if _, err := executor.Exec(ctx, `INSERT INTO ldv_outbox (record) VALUES ($1)`, record); err != nil {
		return fmt.Errorf("append LDV record: %w", err)
	}
	return nil
}

// AppendRecord stores a record on its own, for the operations that read
// rather than mutate. There is nothing to be transactional with, but the
// record still belongs in the same durable store as the rest.
func (s *PostgreSQLStore) AppendRecord(ctx context.Context, record []byte) error {
	return appendOutbox(ctx, s.pool, record)
}

// PendingRecords returns spooled records oldest first, with their ids.
func (s *PostgreSQLStore) PendingRecords(ctx context.Context, limit int) ([]OutboxEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, record FROM ldv_outbox ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("read LDV outbox: %w", err)
	}
	defer rows.Close()

	entries := make([]OutboxEntry, 0)
	for rows.Next() {
		var entry OutboxEntry
		if err := rows.Scan(&entry.ID, &entry.Record); err != nil {
			return nil, fmt.Errorf("scan LDV outbox: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// MarkDelivered removes a record the logbook has accepted.
func (s *PostgreSQLStore) MarkDelivered(ctx context.Context, id int64) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM ldv_outbox WHERE id = $1`, id); err != nil {
		return fmt.Errorf("clear delivered LDV record: %w", err)
	}
	return nil
}

func (s *PostgreSQLStore) List(ctx context.Context, filter ConsentFilter) ([]*Consent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			consent_id,
			status,
			subject_ref,
			dienstverlener_oin,
			scopes,
			scope_entries,
			use_case,
			created_at,
			valid_until
		FROM consents
		WHERE ($1::text = '' OR subject_ref = $1)
		  AND ($2::text = '' OR scopes @> jsonb_build_array($2::text))
		  AND ($3::text = '' OR status = $3)
		ORDER BY created_at ASC
	`, filter.SubjectRef, filter.Scope, filter.Status)
	if err != nil {
		return nil, fmt.Errorf("query consents: %w", err)
	}
	defer rows.Close()

	consents := make([]*Consent, 0)

	for rows.Next() {
		consent, err := scanConsent(rows)
		if err != nil {
			return nil, err
		}

		consents = append(consents, consent)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate consents: %w", err)
	}

	return consents, nil
}

func (s *PostgreSQLStore) Get(ctx context.Context, consentID string) (*Consent, bool, error) {
	consent, err := scanConsent(s.pool.QueryRow(ctx, `
		SELECT
			consent_id,
			status,
			subject_ref,
			dienstverlener_oin,
			scopes,
			scope_entries,
			use_case,
			created_at,
			valid_until
		FROM consents
		WHERE consent_id = $1
	`, consentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}

		return nil, false, err
	}

	return consent, true, nil
}

// Revoke revokes a consent and stores its LDV record in one transaction. The
// record is built from the revoked consent, so it can only be written once the
// revocation is known to have happened — which is why it takes a function
// rather than the bytes.
func (s *PostgreSQLStore) Revoke(ctx context.Context, consentID string, record func(*Consent) ([]byte, error)) (*Consent, bool, error) {
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	consent, err := scanConsent(transaction.QueryRow(ctx, `
		UPDATE consents
		SET status = 'REVOKED'
		WHERE consent_id = $1
		RETURNING
			consent_id,
			status,
			subject_ref,
			dienstverlener_oin,
			scopes,
			scope_entries,
			use_case,
			created_at,
			valid_until
	`, consentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}

		return nil, false, err
	}

	encoded, err := record(consent)
	if err != nil {
		return nil, false, err
	}
	if err := appendOutbox(ctx, transaction, encoded); err != nil {
		return nil, false, err
	}
	if err := transaction.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit revocation and its record: %w", err)
	}
	return consent, true, nil
}

func scanConsent(row rowScanner) (*Consent, error) {
	consent := &Consent{}
	var subjectRef pgtype.Text
	var scopes []byte
	var scopeEntries []byte

	err := row.Scan(
		&consent.ConsentID,
		&consent.Status,
		&subjectRef,
		&consent.DienstverlenrOIN,
		&scopes,
		&scopeEntries,
		&consent.UseCase,
		&consent.CreatedAt,
		&consent.ValidUntil,
	)
	if err != nil {
		return nil, fmt.Errorf("scan consent: %w", err)
	}
	if subjectRef.Valid {
		consent.SubjectRef = subjectRef.String
	}

	if err := json.Unmarshal(scopes, &consent.Scopes); err != nil {
		return nil, fmt.Errorf("unmarshal scopes: %w", err)
	}

	if err := json.Unmarshal(scopeEntries, &consent.ScopeEntries); err != nil {
		return nil, fmt.Errorf("unmarshal scope entries: %w", err)
	}

	return consent, nil
}
