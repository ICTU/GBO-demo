package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

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
		PI:               "PI-persistent",
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
	if fetched.PI != consent.PI || fetched.Status != "ACTIVE" {
		t.Fatalf("unexpected persisted consent: %+v", fetched)
	}

	filtered, err := reopened.List(ctx, ConsentFilter{
		PI:     consent.PI,
		Scope:  "inkomen:read",
		Status: "ACTIVE",
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
