package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gbo-demo/eudi-credential-issuer/internal/postgres"
	"gbo-demo/eudi-credential-issuer/internal/storetest"
)

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
