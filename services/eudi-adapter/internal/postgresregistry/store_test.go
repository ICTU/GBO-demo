package postgresregistry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

func TestMigrationAndCandidatePersistence(t *testing.T) {
	store := openTestStore(t)
	candidate := postgresTestCandidate("belastingdienst", "bd-offer", time.Now().UTC())
	if err := store.PutCandidate(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := store.PutStatus(context.Background(), onboarding.Status{
		SourceID: candidate.SourceID, State: onboarding.StateRolloutRequired,
		MetadataVersion: candidate.MetadataVersion, DeploymentDigest: candidate.DeploymentDigest,
		TransportAuthenticated: true, CheckedAt: candidate.CheckedAt,
	}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	reopened, err := Open(context.Background(), testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	loaded, ok, err := reopened.Candidate(context.Background(), candidate.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || loaded.MetadataETag != candidate.MetadataETag || len(loaded.TypeMetadata) != 1 {
		t.Fatalf("candidate did not survive restart: ok=%v loaded=%+v", ok, loaded)
	}
	var state, metadataVersion string
	var checkedAt time.Time
	if err := reopened.pool.QueryRow(context.Background(), `
		SELECT state, metadata_version, checked_at FROM source_statuses WHERE source_id = $1
	`, candidate.SourceID).Scan(&state, &metadataVersion, &checkedAt); err != nil {
		t.Fatal(err)
	}
	if state != string(onboarding.StateRolloutRequired) || metadataVersion != candidate.MetadataVersion || !checkedAt.Equal(loaded.CheckedAt) {
		t.Fatalf("status did not survive restart: state=%q version=%q checked_at=%s", state, metadataVersion, checkedAt)
	}
}

func TestMigrateCreatesMissingSchema(t *testing.T) {
	databaseURL := os.Getenv("SOURCE_REGISTRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SOURCE_REGISTRY_TEST_DATABASE_URL is not set")
	}
	schema := fmt.Sprintf("registry_missing_%d", time.Now().UnixNano()%1_000_000_000)
	admin, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
			t.Errorf("drop migration-created schema: %v", err)
		}
	})
	store, err := Open(context.Background(), Options{DatabaseURL: databaseURL, Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := admin.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)`, schema).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("Migrate did not create schema %q", schema)
	}
}

func TestFailedPromotionLeavesPreviousReleaseActive(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	first, err := onboarding.NewSourceRelease(now, []onboarding.SourceCandidate{
		postgresTestCandidate("belastingdienst", "bd-offer", now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Promote(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	failedCandidates := []onboarding.SourceCandidate{
		postgresTestCandidate("belastingdienst", "bd-offer-v2", now.Add(time.Minute)),
		postgresTestCandidate("rvig", "rvig-offer-v2", now.Add(time.Minute)),
	}
	for index := range failedCandidates {
		failedCandidates[index].MetadataVersion = "2.0"
	}
	failed, err := onboarding.NewSourceRelease(now.Add(time.Minute), failedCandidates)
	if err != nil {
		t.Fatal(err)
	}
	// Fail on the second source insert, after the release header and first
	// source have been written in the transaction.
	if _, err := store.pool.Exec(context.Background(), `
		CREATE FUNCTION reject_rvig_release_source() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.source_id = 'rvig' THEN RAISE EXCEPTION 'injected promotion failure'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER reject_rvig_release_source
		BEFORE INSERT ON source_release_sources
		FOR EACH ROW EXECUTE FUNCTION reject_rvig_release_source();
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.Promote(context.Background(), failed); err == nil {
		t.Fatal("promotion with an injected database failure unexpectedly succeeded")
	}
	active, err := store.ActiveRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != first.ID {
		t.Fatalf("failed promotion changed active release to %q, want %q", active.ID, first.ID)
	}
	if _, err := store.Release(context.Background(), failed.ID); !errors.Is(err, onboarding.ErrReleaseNotFound) {
		t.Fatalf("failed release transaction was retained: %v", err)
	}
}

func TestPromotionRollbackAndConcurrentReads(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	first, err := onboarding.NewSourceRelease(now, []onboarding.SourceCandidate{
		postgresTestCandidate("belastingdienst", "bd-offer", now),
		postgresTestCandidate("rvig", "rvig-offer", now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Promote(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	secondCandidates := []onboarding.SourceCandidate{
		postgresTestCandidate("belastingdienst", "bd-offer-v2", now.Add(time.Minute)),
		postgresTestCandidate("rvig", "rvig-offer-v2", now.Add(time.Minute)),
	}
	for index := range secondCandidates {
		secondCandidates[index].MetadataVersion = "2.0"
		secondCandidates[index].MetadataPayloadDigest = strings.Repeat("c", 64)
		secondCandidates[index].DeploymentDigest = strings.Repeat("d", 64)
	}
	second, err := onboarding.NewSourceRelease(now.Add(time.Minute), secondCandidates)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var readers sync.WaitGroup
	errCh := make(chan error, 8)
	for reader := 0; reader < 8; reader++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				release, err := store.ActiveRelease(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					errCh <- err
					return
				}
				if release.ID != first.ID && release.ID != second.ID {
					errCh <- fmt.Errorf("reader observed unexpected release %q", release.ID)
					return
				}
				if len(release.Sources) != 2 {
					errCh <- fmt.Errorf("reader observed partial release with %d sources", len(release.Sources))
					return
				}
				if release.Sources[0].MetadataVersion != release.Sources[1].MetadataVersion {
					errCh <- fmt.Errorf("reader observed mixed releases: %+v", release.Sources)
					return
				}
			}
		}()
	}
	if err := store.Promote(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	cancel()
	readers.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	if err := store.ActivateRelease(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActiveRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != first.ID {
		t.Fatalf("rollback activated %q, want %q", active.ID, first.ID)
	}
	if err := store.ActivateRelease(context.Background(), strings.Repeat("f", 64)); !errors.Is(err, onboarding.ErrReleaseNotFound) {
		t.Fatalf("ActivateRelease() error = %v, want ErrReleaseNotFound", err)
	}
}

func TestFreshnessRefreshReusesReleaseAndUpdatesActiveLifecycle(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	candidate := postgresTestCandidate("belastingdienst", "offer", now)
	first, err := onboarding.NewSourceRelease(now, []onboarding.SourceCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Promote(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	refreshedCandidate := candidate
	refreshedCandidate.CheckedAt = now.Add(30 * time.Second)
	refreshedCandidate.FreshUntil = candidate.FreshUntil.Add(30 * time.Second)
	refreshedCandidate.StaleUntil = candidate.StaleUntil.Add(30 * time.Second)
	refreshed, err := onboarding.NewSourceRelease(now.Add(30*time.Second), []onboarding.SourceCandidate{refreshedCandidate})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.ID != first.ID {
		t.Fatalf("freshness refresh created release %q, want %q", refreshed.ID, first.ID)
	}
	if err := store.Promote(context.Background(), refreshed); err != nil {
		t.Fatal(err)
	}

	var releases int
	if err := store.pool.QueryRow(context.Background(), `SELECT count(*) FROM source_releases`).Scan(&releases); err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatalf("freshness refresh stored %d releases, want 1", releases)
	}
	state, found, err := store.ActiveReleaseState(context.Background())
	if err != nil || !found {
		t.Fatalf("active release state: found=%v err=%v", found, err)
	}
	if state.ReleaseID != first.ID || len(state.Sources) != 1 || !state.Sources[0].StaleUntil.Equal(refreshedCandidate.StaleUntil) {
		t.Fatalf("active release state was not refreshed: %+v", state)
	}
	active, err := store.ActiveRelease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !active.Sources[0].CheckedAt.Equal(refreshedCandidate.CheckedAt) || !active.Sources[0].StaleUntil.Equal(refreshedCandidate.StaleUntil) {
		t.Fatalf("active lifecycle was not refreshed: %+v", active.Sources[0])
	}
}

func TestDatabaseContainsNoPrivateKeyMaterial(t *testing.T) {
	store := openTestStore(t)
	candidate := postgresTestCandidate("belastingdienst", "offer", time.Now().UTC())
	candidate.Snapshot = json.RawMessage(`{"private_key":"secret"}`)
	if err := store.PutCandidate(context.Background(), candidate); err == nil {
		t.Fatal("PutCandidate accepted private key field")
	}
	var found bool
	err := store.pool.QueryRow(context.Background(), `
		SELECT EXISTS (
			SELECT 1 FROM source_candidates
			WHERE snapshot::text ILIKE '%private_key%' OR snapshot::text ILIKE '%BEGIN PRIVATE KEY%'
		)
	`).Scan(&found)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("private key material was persisted")
	}
}

func TestGrantReadOnlyExposesOnlyRuntimeReleaseTables(t *testing.T) {
	store := openTestStore(t)
	role := fmt.Sprintf("registry_reader_%d", time.Now().UnixNano()%1_000_000_000)
	roleIdentifier := pgx.Identifier{role}.Sanitize()
	if _, err := store.pool.Exec(context.Background(), `CREATE ROLE `+roleIdentifier); err != nil {
		t.Fatal(err)
	}
	// Reproduce the overly broad default grant used by the original Compose
	// bootstrap. GrantReadOnly must correct an existing deployment as well as
	// configure a fresh one.
	if _, err := store.pool.Exec(context.Background(), `GRANT SELECT ON ALL TABLES IN SCHEMA `+pgx.Identifier{testOptions(t).Schema}.Sanitize()+` TO `+roleIdentifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := store.pool.Exec(context.Background(), `DROP OWNED BY `+roleIdentifier); err != nil {
			t.Errorf("drop reader grants: %v", err)
		}
		if _, err := store.pool.Exec(context.Background(), `DROP ROLE `+roleIdentifier); err != nil {
			t.Errorf("drop reader role: %v", err)
		}
	})
	if err := store.GrantReadOnly(context.Background(), role); err != nil {
		t.Fatal(err)
	}
	options := testOptions(t)
	var schemaUsage, releaseSelect, candidateSelect bool
	if err := store.pool.QueryRow(context.Background(), `
		SELECT has_schema_privilege($1, $2, 'USAGE'),
		       has_table_privilege($1, $3, 'SELECT'),
		       has_table_privilege($1, $4, 'SELECT')
	`, role, options.Schema, options.Schema+".source_releases", options.Schema+".source_candidates").Scan(
		&schemaUsage, &releaseSelect, &candidateSelect,
	); err != nil {
		t.Fatal(err)
	}
	if !schemaUsage || !releaseSelect || candidateSelect {
		t.Fatalf("unexpected reader grants: schema=%v releases=%v candidates=%v", schemaUsage, releaseSelect, candidateSelect)
	}
	if err := store.GrantReadOnly(context.Background(), `reader"; DROP SCHEMA public; --`); err == nil {
		t.Fatal("GrantReadOnly accepted an unsafe role identifier")
	}
}

var testStoreOptions sync.Map

func openTestStore(t *testing.T) *Store {
	t.Helper()
	options := testOptions(t)
	store, err := Open(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func testOptions(t *testing.T) Options {
	t.Helper()
	if value, ok := testStoreOptions.Load(t.Name()); ok {
		return value.(Options)
	}
	databaseURL := os.Getenv("SOURCE_REGISTRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SOURCE_REGISTRY_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "registry_test_" + strings.ToLower(strings.NewReplacer("/", "_", " ", "_", "-", "_").Replace(t.Name()))
	if len(schema) > 55 {
		schema = schema[:55]
	}
	schema += fmt.Sprintf("_%d", time.Now().UnixNano()%1_000_000)
	if !schemaNamePattern.MatchString(schema) {
		t.Fatalf("generated invalid test schema %q", schema)
	}
	if _, err := admin.Exec(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	options := Options{DatabaseURL: databaseURL, Schema: schema}
	testStoreOptions.Store(t.Name(), options)
	return options
}

func postgresTestCandidate(sourceID, offerKey string, now time.Time) onboarding.SourceCandidate {
	metadataBytes := []byte(`{"name":"example"}`)
	metadataDigest := sha256.Sum256(metadataBytes)
	certificates := make([]onboarding.PublicCertificate, 0, 5)
	for _, role := range []string{"issuer", "reader", "status", "issuer_ca", "reader_ca"} {
		certificates = append(certificates, onboarding.PublicCertificate{
			Role: role, Subject: "CN=" + sourceID + " " + role, SHA256: strings.Repeat("c", 64), NotAfter: now.AddDate(1, 0, 0),
		})
	}
	return onboarding.SourceCandidate{
		SourceID: sourceID, MetadataVersion: "1.0", MetadataPayloadDigest: strings.Repeat("a", 64),
		MetadataETag: `"etag"`, DeploymentDigest: strings.Repeat("b", 64), CheckedAt: now,
		ExpiresAt: now.Add(2 * time.Hour), FreshUntil: now.Add(15 * time.Minute), StaleUntil: now.Add(time.Hour),
		TransportAuthenticated: true,
		Snapshot:               json.RawMessage(`{"source":{"source_id":"` + sourceID + `"}}`),
		Offers:                 json.RawMessage(`[{"key":"` + offerKey + `"}]`),
		CertificateSet:         onboarding.PublicCertificateSet{ID: sourceID, Certificates: certificates},
		TypeMetadata: []onboarding.TypeMetadata{{
			VCT: "https://example.test/types/" + sourceID + "/example/v1.0", Version: "1.0",
			Integrity: "sha256-" + base64.StdEncoding.EncodeToString(metadataDigest[:]), MediaType: "application/json", Bytes: metadataBytes,
		}},
	}
}
