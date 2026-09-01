// Package postgresregistry implements onboarding's Source Registry ports with
// PostgreSQL. Domain and release decisions remain in the onboarding core.
package postgresregistry

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

var schemaNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type Options struct {
	DatabaseURL string
	Schema      string
}

type Store struct {
	pool   *pgxpool.Pool
	schema string
}

type rowScanner interface {
	Scan(...any) error
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func Open(ctx context.Context, options Options) (*Store, error) {
	if strings.TrimSpace(options.DatabaseURL) == "" {
		return nil, fmt.Errorf("source registry database URL is required")
	}
	config, err := pgxpool.ParseConfig(options.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse source registry database URL: %w", err)
	}
	if options.Schema != "" {
		if !schemaNamePattern.MatchString(options.Schema) {
			return nil, fmt.Errorf("source registry schema %q is invalid", options.Schema)
		}
		config.ConnConfig.RuntimeParams["search_path"] = options.Schema
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create source registry connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to source registry: %w", err)
	}
	return &Store{pool: pool, schema: options.Schema}, nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("source registry store is not open")
	}
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock(hashtext('gbo_source_registry_migrations'))`); err != nil {
		return fmt.Errorf("lock source registry migrations: %w", err)
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('gbo_source_registry_migrations'))`)
	}()
	if s.schema != "" {
		if _, err := connection.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+pgx.Identifier{s.schema}.Sanitize()); err != nil {
			return fmt.Errorf("create Source Registry schema: %w", err)
		}
	}
	if _, err := connection.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS source_registry_schema_migrations (
			version integer PRIMARY KEY,
			name text NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("initialize source registry migrations: %w", err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("list source registry migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return fmt.Errorf("migration %q has no numeric prefix", entry.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("migration %q has invalid version: %w", entry.Name(), err)
		}
		var applied bool
		if err := connection.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM source_registry_schema_migrations WHERE version = $1)`, version).Scan(&applied); err != nil {
			return fmt.Errorf("inspect migration %d: %w", version, err)
		}
		if applied {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		tx, err := connection.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO source_registry_schema_migrations (version, name) VALUES ($1, $2)`, version, entry.Name())
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %d (%s): %w", version, entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

// GrantReadOnly gives one runtime role access only to release material,
// lifecycle observations, and the active pointer. Mutable candidates and
// reconciliation statuses remain control-plane-only.
func (s *Store) GrantReadOnly(ctx context.Context, role string) error {
	if !schemaNamePattern.MatchString(role) {
		return fmt.Errorf("source registry reader role %q is invalid", role)
	}
	if s.schema == "" {
		return fmt.Errorf("source registry schema is required for reader grants")
	}
	var database string
	if err := s.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return fmt.Errorf("load Source Registry database name: %w", err)
	}
	roleIdentifier := pgx.Identifier{role}.Sanitize()
	schemaIdentifier := pgx.Identifier{s.schema}.Sanitize()
	databaseIdentifier := pgx.Identifier{database}.Sanitize()
	statements := []string{
		`GRANT CONNECT ON DATABASE ` + databaseIdentifier + ` TO ` + roleIdentifier,
		`GRANT USAGE ON SCHEMA ` + schemaIdentifier + ` TO ` + roleIdentifier,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA ` + schemaIdentifier + ` REVOKE SELECT ON TABLES FROM ` + roleIdentifier,
		`REVOKE ALL PRIVILEGES ON TABLE ` + schemaIdentifier + `.source_registry_schema_migrations, ` +
			schemaIdentifier + `.source_candidates, ` + schemaIdentifier + `.source_candidate_type_metadata, ` +
			schemaIdentifier + `.source_statuses FROM ` + roleIdentifier,
		`GRANT SELECT ON TABLE ` + schemaIdentifier + `.source_releases, ` +
			schemaIdentifier + `.source_release_sources, ` + schemaIdentifier + `.source_release_type_metadata, ` +
			schemaIdentifier + `.active_source_release TO ` + roleIdentifier,
	}
	for _, statement := range statements {
		if _, err := s.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("grant Source Registry runtime access to %q: %w", role, err)
		}
	}
	return nil
}

func (s *Store) Candidate(ctx context.Context, sourceID string) (onboarding.SourceCandidate, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return onboarding.SourceCandidate{}, false, fmt.Errorf("begin source candidate read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row := tx.QueryRow(ctx, `
		SELECT source_id, metadata_version, metadata_payload_digest, metadata_etag,
		       deployment_digest, checked_at, expires_at, fresh_until, stale_until,
		       transport_authenticated, snapshot, offers, certificate_set
		FROM source_candidates WHERE source_id = $1
	`, sourceID)
	candidate, err := scanCandidate(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return onboarding.SourceCandidate{}, false, nil
	}
	if err != nil {
		return onboarding.SourceCandidate{}, false, fmt.Errorf("load source candidate %q: %w", sourceID, err)
	}
	metadata, err := loadCandidateTypeMetadata(ctx, tx, sourceID)
	if err != nil {
		return onboarding.SourceCandidate{}, false, err
	}
	candidate.TypeMetadata = metadata
	if err := tx.Commit(ctx); err != nil {
		return onboarding.SourceCandidate{}, false, fmt.Errorf("commit source candidate read: %w", err)
	}
	return candidate, true, nil
}

func (s *Store) PutCandidate(ctx context.Context, candidate onboarding.SourceCandidate) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	certificates, err := json.Marshal(candidate.CertificateSet)
	if err != nil {
		return fmt.Errorf("marshal public certificate set: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin candidate update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO source_candidates (
			source_id, metadata_version, metadata_payload_digest, metadata_etag,
			deployment_digest, checked_at, expires_at, fresh_until, stale_until,
			transport_authenticated, snapshot, offers, certificate_set, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,now())
		ON CONFLICT (source_id) DO UPDATE SET
			metadata_version = EXCLUDED.metadata_version,
			metadata_payload_digest = EXCLUDED.metadata_payload_digest,
			metadata_etag = EXCLUDED.metadata_etag,
			deployment_digest = EXCLUDED.deployment_digest,
			checked_at = EXCLUDED.checked_at,
			expires_at = EXCLUDED.expires_at,
			fresh_until = EXCLUDED.fresh_until,
			stale_until = EXCLUDED.stale_until,
			transport_authenticated = EXCLUDED.transport_authenticated,
			snapshot = EXCLUDED.snapshot,
			offers = EXCLUDED.offers,
			certificate_set = EXCLUDED.certificate_set,
			updated_at = now()
	`, candidate.SourceID, candidate.MetadataVersion, candidate.MetadataPayloadDigest, candidate.MetadataETag,
		candidate.DeploymentDigest, candidate.CheckedAt, candidate.ExpiresAt, candidate.FreshUntil,
		candidate.StaleUntil, candidate.TransportAuthenticated, candidate.Snapshot, candidate.Offers, certificates)
	if err != nil {
		return fmt.Errorf("store source candidate %q: %w", candidate.SourceID, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM source_candidate_type_metadata WHERE source_id = $1`, candidate.SourceID); err != nil {
		return fmt.Errorf("replace candidate Type Metadata: %w", err)
	}
	for _, metadata := range candidate.TypeMetadata {
		if _, err := tx.Exec(ctx, `
			INSERT INTO source_candidate_type_metadata (source_id, vct, type_version, integrity, media_type, bytes)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, candidate.SourceID, metadata.VCT, metadata.Version, metadata.Integrity, metadata.MediaType, metadata.Bytes); err != nil {
			return fmt.Errorf("store candidate Type Metadata %q: %w", metadata.VCT, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit source candidate %q: %w", candidate.SourceID, err)
	}
	return nil
}

func (s *Store) PutStatus(ctx context.Context, status onboarding.Status) error {
	if strings.TrimSpace(status.SourceID) == "" || status.CheckedAt.IsZero() {
		return fmt.Errorf("source status is incomplete")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO source_statuses (
			source_id, state, reason, message, metadata_version, deployment_digest,
			transport_authenticated, checked_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (source_id) DO UPDATE SET
			state = EXCLUDED.state, reason = EXCLUDED.reason, message = EXCLUDED.message,
			metadata_version = EXCLUDED.metadata_version,
			deployment_digest = EXCLUDED.deployment_digest,
			transport_authenticated = EXCLUDED.transport_authenticated,
			checked_at = EXCLUDED.checked_at
	`, status.SourceID, status.State, status.Reason, status.Message, status.MetadataVersion,
		status.DeploymentDigest, status.TransportAuthenticated, status.CheckedAt)
	if err != nil {
		return fmt.Errorf("store source status %q: %w", status.SourceID, err)
	}
	return nil
}

func (s *Store) Promote(ctx context.Context, release onboarding.SourceRelease) (bool, error) {
	if err := validateRelease(release); err != nil {
		return false, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, fmt.Errorf("begin source release promotion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM source_releases WHERE release_id = $1)`, release.ID).Scan(&exists); err != nil {
		return false, fmt.Errorf("inspect source release %q: %w", release.ID, err)
	}
	if !exists {
		if _, err := tx.Exec(ctx, `
			INSERT INTO source_releases (release_id, digest, materialization_digest, created_at, offers)
			VALUES ($1,$2,$3,$4,$5)
		`, release.ID, release.Digest, release.MaterializationDigest, release.CreatedAt, release.Offers); err != nil {
			return false, fmt.Errorf("insert source release: %w", err)
		}
		for _, source := range release.Sources {
			certificates, err := json.Marshal(source.CertificateSet)
			if err != nil {
				return false, fmt.Errorf("marshal release certificate set: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO source_release_sources (
					release_id, source_id, metadata_version, metadata_payload_digest, metadata_etag,
					deployment_digest, checked_at, expires_at, fresh_until, stale_until,
					transport_authenticated, snapshot, certificate_set
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			`, release.ID, source.SourceID, source.MetadataVersion, source.MetadataPayloadDigest,
				source.MetadataETag, source.DeploymentDigest, source.CheckedAt, source.ExpiresAt,
				source.FreshUntil, source.StaleUntil, source.TransportAuthenticated, source.Snapshot, certificates); err != nil {
				return false, fmt.Errorf("insert release source %q: %w", source.SourceID, err)
			}
			for _, metadata := range source.TypeMetadata {
				if _, err := tx.Exec(ctx, `
					INSERT INTO source_release_type_metadata (
						release_id, source_id, vct, type_version, integrity, media_type, bytes
					) VALUES ($1,$2,$3,$4,$5,$6,$7)
				`, release.ID, source.SourceID, metadata.VCT, metadata.Version, metadata.Integrity, metadata.MediaType, metadata.Bytes); err != nil {
					return false, fmt.Errorf("insert release Type Metadata %q: %w", metadata.VCT, err)
				}
			}
		}
	} else {
		// Release material is immutable, but successful 304 checks extend the
		// lifecycle observations used by the runtime. Refresh those rows in
		// place instead of copying the complete release and Type Metadata.
		for _, source := range release.Sources {
			result, err := tx.Exec(ctx, `
				UPDATE source_release_sources
				SET checked_at = $3, expires_at = $4, fresh_until = $5, stale_until = $6
				WHERE release_id = $1 AND source_id = $2 AND deployment_digest = $7
			`, release.ID, source.SourceID, source.CheckedAt, source.ExpiresAt,
				source.FreshUntil, source.StaleUntil, source.DeploymentDigest)
			if err != nil {
				return false, fmt.Errorf("refresh release source %q lifecycle: %w", source.SourceID, err)
			}
			if result.RowsAffected() != 1 {
				return false, fmt.Errorf("refresh release source %q lifecycle: immutable release contents differ", source.SourceID)
			}
		}
	}
	activated := true
	if exists {
		// The conditional upsert takes the active-pointer row lock and decides
		// atomically whether an operator has selected another stored release.
		// Reconciliation may refresh lifecycle data, but must not undo rollback.
		result, err := tx.Exec(ctx, `
			INSERT INTO active_source_release (singleton, release_id) VALUES (1, $1)
			ON CONFLICT (singleton) DO UPDATE SET release_id = EXCLUDED.release_id
			WHERE active_source_release.release_id = EXCLUDED.release_id
		`, release.ID)
		if err != nil {
			return false, fmt.Errorf("preserve or activate source release %q: %w", release.ID, err)
		}
		activated = result.RowsAffected() == 1
	} else if _, err := tx.Exec(ctx, `
		INSERT INTO active_source_release (singleton, release_id) VALUES (1, $1)
		ON CONFLICT (singleton) DO UPDATE SET release_id = EXCLUDED.release_id
	`, release.ID); err != nil {
		return false, fmt.Errorf("activate source release %q: %w", release.ID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit source release promotion: %w", err)
	}
	return activated, nil
}

func (s *Store) ActiveReleaseState(ctx context.Context) (onboarding.ActiveReleaseState, bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT active.release_id, source.source_id, source.checked_at, source.expires_at,
		       source.fresh_until, source.stale_until
		FROM active_source_release AS active
		JOIN source_release_sources AS source ON source.release_id = active.release_id
		WHERE active.singleton = 1
		ORDER BY source.source_id
	`)
	if err != nil {
		return onboarding.ActiveReleaseState{}, false, fmt.Errorf("load active source release state: %w", err)
	}
	defer rows.Close()
	state := onboarding.ActiveReleaseState{}
	for rows.Next() {
		var releaseID string
		var lifecycle onboarding.ReleaseSourceLifecycle
		if err := rows.Scan(&releaseID, &lifecycle.SourceID, &lifecycle.CheckedAt, &lifecycle.ExpiresAt, &lifecycle.FreshUntil, &lifecycle.StaleUntil); err != nil {
			return onboarding.ActiveReleaseState{}, false, fmt.Errorf("scan active source release state: %w", err)
		}
		if state.ReleaseID == "" {
			state.ReleaseID = releaseID
		} else if state.ReleaseID != releaseID {
			return onboarding.ActiveReleaseState{}, false, fmt.Errorf("active source release state is inconsistent")
		}
		state.Sources = append(state.Sources, lifecycle)
	}
	if err := rows.Err(); err != nil {
		return onboarding.ActiveReleaseState{}, false, fmt.Errorf("load active source release state: %w", err)
	}
	if state.ReleaseID == "" {
		return onboarding.ActiveReleaseState{}, false, nil
	}
	return state, true, nil
}

func (s *Store) ActiveRelease(ctx context.Context) (onboarding.SourceRelease, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return onboarding.SourceRelease{}, fmt.Errorf("begin active release read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var releaseID string
	if err := tx.QueryRow(ctx, `SELECT release_id FROM active_source_release WHERE singleton = 1`).Scan(&releaseID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return onboarding.SourceRelease{}, onboarding.ErrNoActiveRelease
		}
		return onboarding.SourceRelease{}, fmt.Errorf("load active source release ID: %w", err)
	}
	release, err := loadRelease(ctx, tx, releaseID)
	if err != nil {
		return onboarding.SourceRelease{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return onboarding.SourceRelease{}, fmt.Errorf("commit active release read: %w", err)
	}
	return release, nil
}

func (s *Store) Release(ctx context.Context, releaseID string) (onboarding.SourceRelease, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return onboarding.SourceRelease{}, fmt.Errorf("begin release read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	release, err := loadRelease(ctx, tx, releaseID)
	if err != nil {
		return onboarding.SourceRelease{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return onboarding.SourceRelease{}, fmt.Errorf("commit release read: %w", err)
	}
	return release, nil
}

func (s *Store) ActivateRelease(ctx context.Context, releaseID string) error {
	result, err := s.pool.Exec(ctx, `
		INSERT INTO active_source_release (singleton, release_id)
		SELECT 1, release_id FROM source_releases WHERE release_id = $1
		ON CONFLICT (singleton) DO UPDATE SET release_id = EXCLUDED.release_id
	`, releaseID)
	if err != nil {
		return fmt.Errorf("activate source release %q: %w", releaseID, err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", onboarding.ErrReleaseNotFound, releaseID)
	}
	return nil
}

func loadRelease(ctx context.Context, query queryer, releaseID string) (onboarding.SourceRelease, error) {
	var release onboarding.SourceRelease
	err := query.QueryRow(ctx, `
		SELECT release_id, digest, materialization_digest, created_at, offers
		FROM source_releases WHERE release_id = $1
	`, releaseID).Scan(&release.ID, &release.Digest, &release.MaterializationDigest, &release.CreatedAt, &release.Offers)
	if errors.Is(err, pgx.ErrNoRows) {
		return onboarding.SourceRelease{}, fmt.Errorf("%w: %s", onboarding.ErrReleaseNotFound, releaseID)
	}
	if err != nil {
		return onboarding.SourceRelease{}, fmt.Errorf("load source release %q: %w", releaseID, err)
	}
	rows, err := query.Query(ctx, `
		SELECT source_id, metadata_version, metadata_payload_digest, metadata_etag,
		       deployment_digest, checked_at, expires_at, fresh_until, stale_until,
		       transport_authenticated, snapshot, certificate_set
		FROM source_release_sources WHERE release_id = $1 ORDER BY source_id
	`, releaseID)
	if err != nil {
		return onboarding.SourceRelease{}, fmt.Errorf("load release sources: %w", err)
	}
	for rows.Next() {
		var source onboarding.ReleaseSource
		var certificates []byte
		if err := rows.Scan(
			&source.SourceID, &source.MetadataVersion, &source.MetadataPayloadDigest, &source.MetadataETag,
			&source.DeploymentDigest, &source.CheckedAt, &source.ExpiresAt, &source.FreshUntil,
			&source.StaleUntil, &source.TransportAuthenticated, &source.Snapshot, &certificates,
		); err != nil {
			return onboarding.SourceRelease{}, fmt.Errorf("scan release source: %w", err)
		}
		if err := json.Unmarshal(certificates, &source.CertificateSet); err != nil {
			return onboarding.SourceRelease{}, fmt.Errorf("decode release certificate set: %w", err)
		}
		release.Sources = append(release.Sources, source)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return onboarding.SourceRelease{}, fmt.Errorf("iterate release sources: %w", err)
	}
	rows.Close()
	for index := range release.Sources {
		release.Sources[index].TypeMetadata, err = loadReleaseTypeMetadata(ctx, query, releaseID, release.Sources[index].SourceID)
		if err != nil {
			return onboarding.SourceRelease{}, err
		}
	}
	return release, nil
}

func scanCandidate(row rowScanner) (onboarding.SourceCandidate, error) {
	var candidate onboarding.SourceCandidate
	var certificates []byte
	err := row.Scan(
		&candidate.SourceID, &candidate.MetadataVersion, &candidate.MetadataPayloadDigest,
		&candidate.MetadataETag, &candidate.DeploymentDigest, &candidate.CheckedAt,
		&candidate.ExpiresAt, &candidate.FreshUntil, &candidate.StaleUntil,
		&candidate.TransportAuthenticated, &candidate.Snapshot, &candidate.Offers, &certificates,
	)
	if err != nil {
		return onboarding.SourceCandidate{}, err
	}
	if err := json.Unmarshal(certificates, &candidate.CertificateSet); err != nil {
		return onboarding.SourceCandidate{}, fmt.Errorf("decode candidate certificate set: %w", err)
	}
	return candidate, nil
}

func loadCandidateTypeMetadata(ctx context.Context, query queryer, sourceID string) ([]onboarding.TypeMetadata, error) {
	return loadTypeMetadata(ctx, query, `
		SELECT vct, type_version, integrity, media_type, bytes
		FROM source_candidate_type_metadata WHERE source_id = $1 ORDER BY vct, type_version
	`, sourceID)
}

func loadReleaseTypeMetadata(ctx context.Context, query queryer, releaseID, sourceID string) ([]onboarding.TypeMetadata, error) {
	return loadTypeMetadata(ctx, query, `
		SELECT vct, type_version, integrity, media_type, bytes
		FROM source_release_type_metadata
		WHERE release_id = $1 AND source_id = $2 ORDER BY vct, type_version
	`, releaseID, sourceID)
}

func loadTypeMetadata(ctx context.Context, query queryer, statement string, arguments ...any) ([]onboarding.TypeMetadata, error) {
	rows, err := query.Query(ctx, statement, arguments...)
	if err != nil {
		return nil, fmt.Errorf("load Type Metadata: %w", err)
	}
	defer rows.Close()
	metadata := make([]onboarding.TypeMetadata, 0)
	for rows.Next() {
		var item onboarding.TypeMetadata
		if err := rows.Scan(&item.VCT, &item.Version, &item.Integrity, &item.MediaType, &item.Bytes); err != nil {
			return nil, fmt.Errorf("scan Type Metadata: %w", err)
		}
		metadata = append(metadata, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Type Metadata: %w", err)
	}
	return metadata, nil
}

func validateRelease(release onboarding.SourceRelease) error {
	return release.Validate()
}

var _ onboarding.SourceRegistry = (*Store)(nil)
