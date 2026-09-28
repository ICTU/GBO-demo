package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gbo-demo/eudi-adapter/internal/onboarding"
	"gbo-demo/eudi-adapter/internal/postgresregistry"
)

// candidateOnlyRegistry serves stored candidates; every other registry call
// panics through the nil embedded interface.
type candidateOnlyRegistry struct {
	onboarding.SourceRegistry
	candidates map[string]onboarding.SourceCandidate
}

func (r candidateOnlyRegistry) Candidate(_ context.Context, sourceID string) (onboarding.SourceCandidate, bool, error) {
	candidate, found := r.candidates[sourceID]
	return candidate, found, nil
}

// A candidate written before the claims-once shape cannot be loaded. The
// reconciler must see it as absent, so it fetches and activates the source
// again, and must not promote it into a release the runtime would reject.
func TestIncompatibleRegistrySnapshotIsTreatedAsAbsent(t *testing.T) {
	registry := candidateOnlyRegistry{candidates: map[string]onboarding.SourceCandidate{
		"rvig": {SourceID: "rvig", Snapshot: json.RawMessage(`{
			"schema_version": "2.0", "source": {"source_id": "rvig"},
			"types": [{"type_id": "akte-van-overlijden", "definition": {"mapping": {"naam": {"pointer": "/naam", "datatype": "string"}}}}]
		}`)},
	}}
	backend := newRegistryActivationBackend(context.Background(), registry)
	if _, err := backend.CurrentCandidate("rvig"); !os.IsNotExist(err) {
		t.Fatalf("CurrentCandidate error = %v, want not-exist", err)
	}
	if err := backend.RequireLoadableCandidates([]string{"rvig"}); err == nil || !strings.Contains(err.Error(), "incompatible schema version") {
		t.Fatalf("RequireLoadableCandidates error = %v, want incompatible snapshot rejection", err)
	}
}

// jsonb reorders object keys. Claims and languages are shown in the order the
// source wrote them, so that order has to survive the registry.
func TestRegistrySnapshotPreservesClaimOrderThroughPostgres(t *testing.T) {
	databaseURL := os.Getenv("SOURCE_REGISTRY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SOURCE_REGISTRY_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("registry_test_claim_order_%d", time.Now().UnixNano()%1_000_000)
	store, err := postgresregistry.Open(ctx, postgresregistry.Options{DatabaseURL: databaseURL, Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		admin, err := pgxpool.New(ctx, databaseURL)
		if err != nil {
			t.Error(err)
			return
		}
		defer admin.Close()
		if _, err := admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile("../brp-graphql-server/config/gbo-source-metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	document, err := decodeSourceMetadataDocument(raw)
	if err != nil {
		t.Fatal(err)
	}
	definition := document.eudiAttestations()[0]
	// Put a language whose tag sorts first last, so jsonb would move it.
	definition.Claims[0].Label = append(definition.Claims[0].Label, localizedString{Lang: "en", Text: "Surname"})

	now := time.Now().UTC().Truncate(time.Second)
	secrets := t.TempDir()
	writeTestDevelopmentCAs(t, secrets, now)
	registration := sourceRegistration{
		SourceID: "rvig", SourceOIN: document.SourceOIN, Name: "RvIG",
		MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportUnsecured, Endpoint: "https://metadata.example"},
		DataAccess:       sourceDataAccess{Transport: sourceTransportUnsecured},
	}
	provider := newDevelopmentCAProvider(secrets, "https://issuer.example")
	provider.now = func() time.Time { return now }
	artifacts, err := provider.Provision(registration)
	if err != nil {
		t.Fatal(err)
	}
	certificateSet, err := publicCertificateSet(registration.certificateSetID(), artifacts)
	if err != nil {
		t.Fatal(err)
	}
	publication, err := newTypeMetadataPublication("https://issuer.example", registration.SourceID, definition)
	if err != nil {
		t.Fatal(err)
	}
	activation := &sourceActivation{
		SchemaVersion: registrySnapshotSchemaVersion, Source: registration, MetadataURL: "https://metadata.example",
		MetadataVersion: document.Version, MetadataPayloadDigest: strings.Repeat("a", 64), CheckedAt: now,
		ExpiresAt: now.Add(2 * time.Hour), FreshUntil: now.Add(15 * time.Minute), StaleUntil: now.Add(time.Hour),
		Types: []activatedType{{
			TypeID: definition.TypeID, TypeVersion: definition.TypeVersion, VCT: publication.VCT,
			VCTIntegrity: publication.Integrity, Offers: definition.Offers, Definition: definition,
		}},
	}
	candidate, err := registryCandidateFromActivation(activation, certificateSet, []onboarding.TypeMetadata{{
		VCT: publication.VCT, Version: definition.TypeVersion, Integrity: publication.Integrity,
		MediaType: "application/json", Bytes: publication.body,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}

	loaded, err := newRegistryActivationBackend(ctx, store).CurrentCandidate("rvig")
	if err != nil {
		t.Fatal(err)
	}
	restored := loaded.Types[0].Definition
	// Compare encodings: they keep claim and language order but not the
	// whitespace inside rendering, which publication re-encodes anyway.
	got, err := json.Marshal(restored)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("definition changed through PostgreSQL:\n got %s\nwant %s", got, want)
	}
	restoredPublication, err := newTypeMetadataPublication("https://issuer.example", registration.SourceID, restored)
	if err != nil {
		t.Fatal(err)
	}
	if restoredPublication.Integrity != publication.Integrity {
		t.Fatal("Type Metadata regenerated from the stored definition has different bytes")
	}
}
