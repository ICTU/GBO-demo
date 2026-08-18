package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

type legacyNullSubjectRow struct {
	now time.Time
}

func (r legacyNullSubjectRow) Scan(dest ...any) error {
	*dest[0].(*string) = "legacy-consent"
	*dest[1].(*string) = "ACTIVE"
	*dest[2].(*pgtype.Text) = pgtype.Text{Valid: false}
	*dest[3].(*string) = "00000001234567890000"
	*dest[4].(*[]byte) = []byte(`["inkomen:read"]`)
	*dest[5].(*[]byte) = []byte(`[]`)
	*dest[6].(*string) = "hypotheek"
	*dest[7].(*time.Time) = r.now
	*dest[8].(*time.Time) = r.now.Add(time.Hour)
	return nil
}

func TestScanConsentHandlesLegacyNullSubjectRef(t *testing.T) {
	consent, err := scanConsent(legacyNullSubjectRow{now: time.Now().UTC()})
	if err != nil {
		t.Fatalf("scan legacy consent: %v", err)
	}
	if consent.SubjectRef != "" {
		t.Fatalf("subject_ref = %q, want empty legacy value", consent.SubjectRef)
	}
	if consent.ConsentID != "legacy-consent" || consent.Status != "ACTIVE" {
		t.Fatalf("unexpected legacy consent: %+v", consent)
	}
}

func TestPostgreSQLStorePersistsConsent(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	store, err := NewPostgreSQLStore(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create PostgreSQL store: %v", err)
	}

	consentID := "test-" + uuid.NewString()
	t.Cleanup(func() {
		cleanupStore, cleanupErr := NewPostgreSQLStore(ctx, databaseURL)
		if cleanupErr != nil {
			t.Errorf("create cleanup store: %v", cleanupErr)

			return
		}
		defer cleanupStore.Close()

		if _, cleanupErr = cleanupStore.pool.Exec(ctx, "DELETE FROM consents WHERE consent_id = $1", consentID); cleanupErr != nil {
			t.Errorf("delete test consent: %v", cleanupErr)
		}
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	consent := &Consent{
		ConsentID:        consentID,
		Status:           "ACTIVE",
		SubjectRef:       "EP-portal-persistent",
		DienstverlenrOIN: "00000001234567890000",
		Scopes:           []string{"inkomen:read"},
		ScopeEntries: []ScopeEntry{{
			Bronhouder:      "MinBZK",
			ScopeID:         "inkomen:read",
			ConsentedFields: []string{"belastingjaar", "verzamelinkomen"},
		}},
		UseCase:    "hypotheek",
		CreatedAt:  now,
		ValidUntil: now.Add(24 * time.Hour),
	}

	if err := store.Create(ctx, consent); err != nil {
		t.Fatalf("create consent: %v", err)
	}
	store.Close()

	reopened, err := NewPostgreSQLStore(ctx, databaseURL)
	if err != nil {
		t.Fatalf("reopen PostgreSQL store: %v", err)
	}
	defer reopened.Close()

	fetched, ok, err := reopened.Get(ctx, consentID)
	if err != nil {
		t.Fatalf("get consent: %v", err)
	}
	if !ok {
		t.Fatal("persisted consent not found after reopening store")
	}
	if fetched.SubjectRef != consent.SubjectRef || fetched.Status != "ACTIVE" {
		t.Fatalf("unexpected persisted consent: %+v", fetched)
	}

	filtered, err := reopened.List(ctx, ConsentFilter{
		SubjectRef: consent.SubjectRef,
		Scope:      "inkomen:read",
		Status:     "ACTIVE",
	})
	if err != nil {
		t.Fatalf("list consent: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ConsentID != consentID {
		t.Fatalf("unexpected filtered consents: %+v", filtered)
	}

	revoked, ok, err := reopened.Revoke(ctx, consentID)
	if err != nil {
		t.Fatalf("revoke consent: %v", err)
	}
	if !ok || revoked.Status != "REVOKED" {
		t.Fatalf("unexpected revoked consent: %+v", revoked)
	}
}

func TestPostgreSQLStoreMigratesLegacyNullSubjectRef(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	seedStore, err := NewPostgreSQLStore(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create seed store: %v", err)
	}
	consentID := "legacy-null-" + uuid.NewString()
	t.Cleanup(func() {
		cleanupStore, cleanupErr := NewPostgreSQLStore(ctx, databaseURL)
		if cleanupErr != nil {
			t.Errorf("create cleanup store: %v", cleanupErr)
			return
		}
		defer cleanupStore.Close()
		if _, cleanupErr = cleanupStore.pool.Exec(ctx, "DELETE FROM consents WHERE consent_id = $1", consentID); cleanupErr != nil {
			t.Errorf("delete legacy test consent: %v", cleanupErr)
		}
	})
	if _, err = seedStore.pool.Exec(ctx, "ALTER TABLE consents ALTER COLUMN subject_ref DROP NOT NULL"); err != nil {
		seedStore.Close()
		t.Fatalf("make subject_ref nullable: %v", err)
	}
	_, err = seedStore.pool.Exec(ctx, `
		INSERT INTO consents (
			consent_id, status, subject_ref, dienstverlener_oin, scopes,
			scope_entries, use_case, created_at, valid_until
		) VALUES ($1, 'ACTIVE', NULL, $2, '["inkomen:read"]', '[]', 'hypotheek', NOW(), NOW() + INTERVAL '1 hour')
	`, consentID, "00000001234567890000")
	seedStore.Close()
	if err != nil {
		t.Fatalf("insert legacy consent: %v", err)
	}

	migrated, err := NewPostgreSQLStore(ctx, databaseURL)
	if err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	defer migrated.Close()

	fetched, ok, err := migrated.Get(ctx, consentID)
	if err != nil {
		t.Fatalf("get migrated consent: %v", err)
	}
	if !ok || fetched.SubjectRef != "" {
		t.Fatalf("unexpected migrated consent: %+v", fetched)
	}

	var nullable string
	var hasDefault bool
	err = migrated.pool.QueryRow(ctx, `
		SELECT is_nullable, column_default IS NOT NULL
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'consents'
		  AND column_name = 'subject_ref'
	`).Scan(&nullable, &hasDefault)
	if err != nil {
		t.Fatalf("inspect subject_ref schema: %v", err)
	}
	if nullable != "NO" || !hasDefault {
		t.Fatalf("subject_ref schema nullable=%q has_default=%v", nullable, hasDefault)
	}
}
