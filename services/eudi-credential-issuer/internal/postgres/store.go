// Package postgres keeps the issuer's state in PostgreSQL, so the issuer
// processes are stateless and can run as several replicas.
package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var schemaNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// allocateAttempts bounds the random probes for a free status index.
const allocateAttempts = 64

// Store implements issuance.Sessions, issuance.OneTime and
// issuance.StatusLists.
type Store struct {
	pool   *pgxpool.Pool
	schema string
}

// Open connects to databaseURL and uses schema for all tables.
func Open(ctx context.Context, databaseURL, schema string) (*Store, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("database URL is required")
	}
	if !schemaNamePattern.MatchString(schema) {
		return nil, fmt.Errorf("schema %q is invalid", schema)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{pool: pool, schema: schema}, nil
}

// Close closes the pool.
func (s *Store) Close() { s.pool.Close() }

// Migrate creates the schema and applies the pending migrations, under an
// advisory lock so concurrent starts do not race.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('gbo_credential_issuer_migrations'))`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('gbo_credential_issuer_migrations'))`)
	}()
	if _, err := conn.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+pgx.Identifier{s.schema}.Sanitize()); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("initialize migrations: %w", err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			return fmt.Errorf("migration %q has no numeric prefix", entry.Name())
		}
		var applied bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&applied); err != nil {
			return fmt.Errorf("inspect migration %d: %w", version, err)
		}
		if applied {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, version, entry.Name())
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

// Purge removes expired state. Sessions hold personal data; they are kept
// until their access tokens can no longer use them, and no longer.
func (s *Store) Purge(ctx context.Context, now time.Time, tokenTTL time.Duration) error {
	for _, q := range []string{
		`DELETE FROM one_time_values WHERE expires_at <= $1`,
		`DELETE FROM seen_identifiers WHERE expires_at <= $1`,
		`DELETE FROM access_tokens WHERE expires_at <= $1`,
		`DELETE FROM disclosure_sessions WHERE expires_at <= $1`,
	} {
		if _, err := s.pool.Exec(ctx, q, now); err != nil {
			return err
		}
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1
		AND NOT EXISTS (SELECT 1 FROM access_tokens t WHERE t.session_id = sessions.id)`, now.Add(-tokenTTL))
	return err
}

func (s *Store) CreateSession(ctx context.Context, sess issuance.Session) error {
	docs, err := json.Marshal(sess.Documents)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO sessions (id, provider, profile, code_hash, expires_at, reference, documents)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sess.ID, sess.Provider, sess.Profile, sess.CodeHash, sess.ExpiresAt, sess.Reference, docs)
	return err
}

func (s *Store) RedeemCode(ctx context.Context, provider, profile, codeHash string, now time.Time) (issuance.Session, error) {
	var sess issuance.Session
	var docs []byte
	err := s.pool.QueryRow(ctx, `UPDATE sessions SET code_used = true
		WHERE code_hash = $1 AND provider = $2 AND profile = $3 AND NOT code_used AND expires_at > $4
		RETURNING id, provider, profile, code_hash, expires_at, reference, documents`, codeHash, provider, profile, now).
		Scan(&sess.ID, &sess.Provider, &sess.Profile, &sess.CodeHash, &sess.ExpiresAt, &sess.Reference, &docs)
	if errors.Is(err, pgx.ErrNoRows) {
		return issuance.Session{}, issuance.ErrNotFound
	}
	if err != nil {
		return issuance.Session{}, err
	}
	return sess, decodeDocuments(docs, &sess)
}

// decodeDocuments keeps numbers as json.Number, so whole numbers beyond
// 2^53 survive the round trip.
func decodeDocuments(raw []byte, sess *issuance.Session) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&sess.Documents); err != nil {
		return fmt.Errorf("decode session documents: %w", err)
	}
	return nil
}

func (s *Store) SaveToken(ctx context.Context, t issuance.AccessToken) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO access_tokens (hash, session_id, jkt, client_id, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		t.Hash, t.SessionID, t.JKT, t.ClientID, t.ExpiresAt)
	return err
}

func (s *Store) Token(ctx context.Context, tokenHash string, now time.Time) (issuance.AccessToken, issuance.Session, error) {
	t := issuance.AccessToken{Hash: tokenHash}
	var sess issuance.Session
	var docs []byte
	err := s.pool.QueryRow(ctx, `SELECT t.session_id, t.jkt, t.client_id, t.expires_at,
			s.id, s.provider, s.profile, s.code_hash, s.expires_at, s.reference, s.documents
		FROM access_tokens t JOIN sessions s ON s.id = t.session_id
		WHERE t.hash = $1 AND t.expires_at > $2`, tokenHash, now).
		Scan(&t.SessionID, &t.JKT, &t.ClientID, &t.ExpiresAt, &sess.ID, &sess.Provider, &sess.Profile, &sess.CodeHash, &sess.ExpiresAt, &sess.Reference, &docs)
	if errors.Is(err, pgx.ErrNoRows) {
		return issuance.AccessToken{}, issuance.Session{}, issuance.ErrNotFound
	}
	if err != nil {
		return issuance.AccessToken{}, issuance.Session{}, err
	}
	if err := decodeDocuments(docs, &sess); err != nil {
		return issuance.AccessToken{}, issuance.Session{}, err
	}
	return t, sess, nil
}

func (s *Store) ReserveIssue(ctx context.Context, sessionID, credentialID, owner string, now, until time.Time) (bool, error) {
	// One statement: a new row is a reservation; an existing one is taken
	// over only when it is a reservation that has expired.
	var reserved bool
	err := s.pool.QueryRow(ctx, `INSERT INTO issued_credentials (session_id, credential_id, state, reserved_until, owner)
		VALUES ($1, $2, 'reserved', $4, $5)
		ON CONFLICT (session_id, credential_id) DO UPDATE SET reserved_until = EXCLUDED.reserved_until, owner = EXCLUDED.owner
		WHERE issued_credentials.state = 'reserved' AND issued_credentials.reserved_until <= $3
		RETURNING true`, sessionID, credentialID, now, until, owner).Scan(&reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return reserved, err
}

func (s *Store) CompleteIssue(ctx context.Context, sessionID, credentialID, owner string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE issued_credentials SET state = 'issued', reserved_until = NULL
		WHERE session_id = $1 AND credential_id = $2 AND state = 'reserved' AND owner = $3`, sessionID, credentialID, owner)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) ReleaseIssue(ctx context.Context, sessionID, credentialID, owner string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM issued_credentials
		WHERE session_id = $1 AND credential_id = $2 AND state = 'reserved' AND owner = $3`, sessionID, credentialID, owner)
	return err
}

func (s *Store) Put(ctx context.Context, kind, value string, expiry time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO one_time_values (kind, value, expires_at) VALUES ($1, $2, $3)`, kind, value, expiry)
	return err
}

func (s *Store) Take(ctx context.Context, kind, value string, now time.Time) (bool, error) {
	var valid bool
	err := s.pool.QueryRow(ctx, `DELETE FROM one_time_values WHERE kind = $1 AND value = $2 RETURNING expires_at > $3`,
		kind, value, now).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return valid, err
}

func (s *Store) FirstUse(ctx context.Context, kind, id string, expiry, now time.Time) (bool, error) {
	// A row that has expired may be taken over; a live row means a replay.
	var inserted bool
	err := s.pool.QueryRow(ctx, `INSERT INTO seen_identifiers (kind, id, expires_at) VALUES ($1, $2, $3)
		ON CONFLICT (kind, id) DO UPDATE SET expires_at = EXCLUDED.expires_at WHERE seen_identifiers.expires_at <= $4
		RETURNING true`, kind, id, expiry, now).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return inserted, err
}

func (s *Store) Allocate(ctx context.Context, list string, size int, reference string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockReference(ctx, tx, reference); err != nil {
		return 0, err
	}
	idx, err := allocate(ctx, tx, list, size, reference)
	if err != nil {
		return 0, err
	}
	return idx, tx.Commit(ctx)
}

// lockReference serialises allocation and revocation for one reference,
// until the transaction ends. Without it an allocation that read "not
// revoked" could commit after a revocation had already updated the
// entries.
func lockReference(ctx context.Context, tx pgx.Tx, reference string) error {
	if reference == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('gbo_issuer_reference:' || $1))`, reference)
	return err
}

func allocate(ctx context.Context, tx pgx.Tx, list string, size int, reference string) (int, error) {
	for range allocateAttempts {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(size)))
		if err != nil {
			return 0, err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO status_entries (list, idx, reference, revoked)
			VALUES ($1, $2, $3, $3 <> '' AND EXISTS (SELECT 1 FROM revoked_references WHERE reference = $3))
			ON CONFLICT DO NOTHING`, list, n.Int64(), reference)
		if err != nil {
			return 0, err
		}
		if tag.RowsAffected() == 1 {
			return int(n.Int64()), nil
		}
	}
	// The list is nearly full: pick at random among the free indices. A
	// concurrent allocation can take the same index; then try again until
	// none is free.
	for {
		var free bool
		if err := tx.QueryRow(ctx, `SELECT count(*) < $2 FROM status_entries WHERE list = $1`, list, size).Scan(&free); err != nil {
			return 0, err
		}
		if !free {
			return 0, fmt.Errorf("status list %q is full", list)
		}
		var idx int
		err := tx.QueryRow(ctx, `INSERT INTO status_entries (list, idx, reference, revoked)
			SELECT $1, g, $3, $3 <> '' AND EXISTS (SELECT 1 FROM revoked_references WHERE reference = $3)
			FROM generate_series(0, $2 - 1) g
			WHERE NOT EXISTS (SELECT 1 FROM status_entries e WHERE e.list = $1 AND e.idx = g)
			ORDER BY random() LIMIT 1
			ON CONFLICT DO NOTHING
			RETURNING idx`, list, size, reference).Scan(&idx)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		return idx, err
	}
}

func (s *Store) Revoke(ctx context.Context, list string, index int) error {
	tag, err := s.pool.Exec(ctx, `UPDATE status_entries SET revoked = true WHERE list = $1 AND idx = $2`, list, index)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return issuance.ErrNotFound
	}
	return nil
}

func (s *Store) RevokeReference(ctx context.Context, reference string) (int, error) {
	if reference == "" {
		return 0, nil
	}
	// Under the reference lock: entries allocated before are revoked by the
	// update, entries allocated after see the recorded reference and are
	// born revoked (see Allocate).
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockReference(ctx, tx, reference); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO revoked_references (reference) VALUES ($1) ON CONFLICT DO NOTHING`, reference); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE status_entries SET revoked = true WHERE reference = $1 AND NOT revoked`, reference)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), tx.Commit(ctx)
}

func (s *Store) ReferenceRevoked(ctx context.Context, reference string) (bool, error) {
	var revoked bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM revoked_references WHERE reference = $1)`, reference).Scan(&revoked)
	return revoked, err
}

func (s *Store) Revoked(ctx context.Context, list string) ([]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT idx FROM status_entries WHERE list = $1 AND revoked ORDER BY idx`, list)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int])
}

func (s *Store) CreateDisclosure(ctx context.Context, d issuance.DisclosureSession) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO disclosure_sessions
		(id, provider, profile, offer, nonce, state, response_key_id, response_key, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		d.ID, d.Provider, d.Profile, d.Offer, d.Nonce, d.State, d.ResponseKeyID, d.ResponseKey, d.ExpiresAt)
	return err
}

func (s *Store) TakeDisclosure(ctx context.Context, id string, now time.Time) (issuance.DisclosureSession, error) {
	var d issuance.DisclosureSession
	err := s.pool.QueryRow(ctx, `DELETE FROM disclosure_sessions WHERE id = $1
		RETURNING id, provider, profile, offer, nonce, state, response_key_id, response_key, expires_at`, id).
		Scan(&d.ID, &d.Provider, &d.Profile, &d.Offer, &d.Nonce, &d.State, &d.ResponseKeyID, &d.ResponseKey, &d.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return issuance.DisclosureSession{}, issuance.ErrNotFound
	}
	if err != nil {
		return issuance.DisclosureSession{}, err
	}
	if !now.Before(d.ExpiresAt) {
		return issuance.DisclosureSession{}, issuance.ErrNotFound
	}
	return d, nil
}
