package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const consentSchema = `
CREATE TABLE IF NOT EXISTS consents (
    consent_id text PRIMARY KEY,
    status text NOT NULL,
    pi text NOT NULL,
    dienstverlener_oin text NOT NULL,
    scopes jsonb NOT NULL,
    scope_entries jsonb NOT NULL,
    use_case text NOT NULL,
    created_at timestamptz NOT NULL,
    valid_until timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS consents_pi_status_idx
    ON consents (pi, status);
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

func (s *PostgreSQLStore) Create(ctx context.Context, consent *Consent) error {
	scopes, err := json.Marshal(consent.Scopes)
	if err != nil {
		return fmt.Errorf("marshal scopes: %w", err)
	}

	scopeEntries, err := json.Marshal(consent.ScopeEntries)
	if err != nil {
		return fmt.Errorf("marshal scope entries: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO consents (
			consent_id,
			status,
			pi,
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
		consent.PI,
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

	return nil
}

func (s *PostgreSQLStore) List(ctx context.Context, filter ConsentFilter) ([]*Consent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			consent_id,
			status,
			pi,
			dienstverlener_oin,
			scopes,
			scope_entries,
			use_case,
			created_at,
			valid_until
		FROM consents
		WHERE ($1::text = '' OR pi = $1)
		  AND ($2::text = '' OR scopes @> jsonb_build_array($2::text))
		  AND ($3::text = '' OR status = $3)
		ORDER BY created_at ASC
	`, filter.PI, filter.Scope, filter.Status)
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
			pi,
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

func (s *PostgreSQLStore) Revoke(ctx context.Context, consentID string) (*Consent, bool, error) {
	consent, err := scanConsent(s.pool.QueryRow(ctx, `
		UPDATE consents
		SET status = 'REVOKED'
		WHERE consent_id = $1
		RETURNING
			consent_id,
			status,
			pi,
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

	return consent, true, nil
}

func scanConsent(row rowScanner) (*Consent, error) {
	consent := &Consent{}
	var scopes []byte
	var scopeEntries []byte

	err := row.Scan(
		&consent.ConsentID,
		&consent.Status,
		&consent.PI,
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

	if err := json.Unmarshal(scopes, &consent.Scopes); err != nil {
		return nil, fmt.Errorf("unmarshal scopes: %w", err)
	}

	if err := json.Unmarshal(scopeEntries, &consent.ScopeEntries); err != nil {
		return nil, fmt.Errorf("unmarshal scope entries: %w", err)
	}

	return consent, nil
}
