package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/postgres"
	"gbo-demo/eudi-credential-issuer/internal/storetest"
)

// openTestStore opens a store in a schema of its own, dropped afterwards, or
// skips the test without ISSUER_TEST_DATABASE_URL.
func openTestStore(t *testing.T) (*postgres.Store, *pgxpool.Pool, string) {
	t.Helper()
	databaseURL := os.Getenv("ISSUER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ISSUER_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("issuer_test_%d", time.Now().UnixNano())
	s, err := postgres.Open(ctx, databaseURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_, _ = admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		admin.Close()
	})
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s, admin, schema
}

// The purge deletes what has expired and keeps the rest: no codes, tokens,
// pending authorizations, disclosure sessions or claims stay longer than
// they are needed (OpenID4VCI 1.0 section 15.3). A session outlives its
// expiry by the token lifetime while a token still points at it.
func TestPurge(t *testing.T) {
	s, admin, schema := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	tokenTTL := 10 * time.Minute
	for _, sess := range []issuance.Session{
		{ID: "gone", Provider: "tax", Profile: "eudi", CodeHash: "c1", ExpiresAt: now.Add(-tokenTTL - time.Minute)},
		{ID: "recent", Provider: "tax", Profile: "eudi", CodeHash: "c2", ExpiresAt: past},
		{ID: "live", Provider: "tax", Profile: "eudi", CodeHash: "c3", ExpiresAt: future},
	} {
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	for _, tok := range []issuance.AccessToken{
		{Hash: "t-old", SessionID: "live", ExpiresAt: past},
		{Hash: "t-new", SessionID: "live", ExpiresAt: future},
	} {
		if err := s.SaveToken(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []issuance.PendingAuthorization{
		{Kind: "par", ID: "p-old", Payload: []byte(`{}`), ExpiresAt: past},
		{Kind: "par", ID: "p-new", Payload: []byte(`{}`), ExpiresAt: future},
	} {
		if err := s.PutPending(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for id, expiry := range map[string]time.Time{"o-old": past, "o-new": future} {
		if _, err := s.FirstUse(ctx, "nonce", id, expiry, now.Add(-2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Purge(ctx, now, tokenTTL); err != nil {
		t.Fatal(err)
	}
	count := func(table, where string) int {
		var n int
		q := `SELECT count(*) FROM ` + pgx.Identifier{schema, table}.Sanitize() + ` WHERE ` + where
		if err := admin.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, c := range []struct {
		table, where string
		want         int
	}{
		{"sessions", "id = 'gone'", 0},
		{"sessions", "id IN ('recent', 'live')", 2},
		{"access_tokens", "hash = 't-old'", 0},
		{"access_tokens", "hash = 't-new'", 1},
		{"pending_authorizations", "id = 'p-old'", 0},
		{"pending_authorizations", "id = 'p-new'", 1},
		{"seen_identifiers", "id = 'o-old'", 0},
		{"seen_identifiers", "id = 'o-new'", 1},
	} {
		if got := count(c.table, c.where); got != c.want {
			t.Errorf("%s where %s: %d rows, want %d", c.table, c.where, got, c.want)
		}
	}
}

// TestContract runs against a real database when
// ISSUER_TEST_DATABASE_URL is set; each subtest gets its own schema.
func TestContract(t *testing.T) {
	databaseURL := os.Getenv("ISSUER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("ISSUER_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	storetest.Run(t, func(t *testing.T) storetest.Store {
		schema := fmt.Sprintf("issuer_test_%d", time.Now().UnixNano())
		ctx := context.Background()
		s, err := postgres.Open(ctx, databaseURL, schema)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			s.Close()
			_, _ = admin.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		})
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		// A second run is a no-op.
		if err := s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	})
}
