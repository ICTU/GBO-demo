package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

type runtimeRegistryFake struct {
	mu       sync.Mutex
	active   string
	releases map[string]onboarding.SourceRelease
	err      error
}

func (f *runtimeRegistryFake) ActiveReleaseID(context.Context) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", false, f.err
	}
	return f.active, f.active != "", nil
}

func (f *runtimeRegistryFake) ActiveRelease(context.Context) (onboarding.SourceRelease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return onboarding.SourceRelease{}, f.err
	}
	return f.releases[f.active], nil
}

func TestSourceReleaseRuntimeRefreshesCompleteSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	first := runtimeTestRelease(t, "release-one", "belastingdienst", "offer-one", now)
	second := runtimeTestRelease(t, "release-two", "rvig", "offer-two", now.Add(time.Minute))
	registry := &runtimeRegistryFake{active: first.ID, releases: map[string]onboarding.SourceRelease{first.ID: first, second.ID: second}}
	runtime := &sourceReleaseRuntime{registry: registry, baseConfig: config{}, refreshInterval: time.Second}

	loaded, err := runtime.current(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.releaseID != first.ID || !strings.Contains(string(loaded.offers), "offer-one") || len(loaded.bindings) != 1 {
		t.Fatalf("unexpected first snapshot: %+v", loaded)
	}
	registry.mu.Lock()
	registry.active = second.ID
	registry.mu.Unlock()
	loaded, err = runtime.current(context.Background(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.releaseID != second.ID || !strings.Contains(string(loaded.offers), "offer-two") || strings.Contains(string(loaded.offers), "offer-one") {
		t.Fatalf("runtime mixed releases: %+v", loaded)
	}
	if _, oldBinding := loaded.bindings["belastingdienst\x00example"]; oldBinding {
		t.Fatal("old release binding survived atomic refresh")
	}
	if _, newBinding := loaded.bindings["rvig\x00example"]; !newBinding {
		t.Fatal("new release binding is missing")
	}
}

func TestSourceReleaseRuntimeRetainsPreviousCompleteSnapshotOnRefreshFailure(t *testing.T) {
	now := time.Now().UTC()
	release := runtimeTestRelease(t, "release-one", "belastingdienst", "offer-one", now)
	registry := &runtimeRegistryFake{active: release.ID, releases: map[string]onboarding.SourceRelease{release.ID: release}}
	runtime := &sourceReleaseRuntime{registry: registry, baseConfig: config{}, refreshInterval: time.Second}
	first, err := runtime.current(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	registry.mu.Lock()
	registry.err = errors.New("temporary database failure")
	registry.mu.Unlock()
	retained, err := runtime.current(context.Background(), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if retained != first {
		t.Fatal("refresh failure replaced the previous complete snapshot")
	}
}

func runtimeTestRelease(t *testing.T, releaseID, sourceID, offerID string, now time.Time) onboarding.SourceRelease {
	t.Helper()
	metadataBody := []byte(`{"display":[]}`)
	digest := sha256.Sum256(metadataBody)
	integrity := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
	offer := sourceOffer{ID: offerID, Label: offerID, Parameters: map[string]any{}}
	definition := sourceAttestationDefinition{
		TypeID: "example", TypeVersion: "1.0", Offers: []sourceOffer{offer},
		GraphQL:        sourceGraphQL{Endpoint: "/graphql", Document: "query Example($bsn: String!) { example(bsn: $bsn) { value } }", SubjectVariable: "bsn", ResultPointer: "/data/example"},
		MappingProfile: "gbo-simple-v1", Mapping: map[string]mappingRule{"value": {Pointer: "/value", Datatype: "string"}},
		AttributeSchema: map[string]sourceAttributeSchema{"value": {Type: "string"}},
	}
	activation := &sourceActivation{
		SchemaVersion: "2.0", Source: sourceRegistration{
			SourceID: sourceID, SourceOIN: "99999999900000000200", Name: sourceID,
			MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportUnsecured, Endpoint: "https://metadata.example"},
			DataAccess:       sourceDataAccess{Transport: sourceTransportUnsecured},
		},
		MetadataURL: "https://metadata.example", MetadataVersion: "1.0",
		MetadataPayloadDigest: strings.Repeat("a", 64), CheckedAt: now,
		ExpiresAt: now.Add(2 * time.Hour), FreshUntil: now.Add(15 * time.Minute), StaleUntil: now.Add(time.Hour),
		Types: []activatedType{{
			TypeID: "example", TypeVersion: "1.0", VCT: "https://adapter.example/types/" + sourceID + "/example/v1.0",
			VCTIntegrity: integrity, Offers: []sourceOffer{offer}, Definition: definition,
		}},
	}
	snapshot, err := registrySnapshotFromActivation(activation)
	if err != nil {
		t.Fatal(err)
	}
	snapshotBody, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	offers, err := json.Marshal([]publicIssuanceOffer{{
		Key: offerID, Label: offerID, AttestationType: activation.Types[0].VCT,
		SourceID: sourceID, SourceOIN: activation.Source.SourceOIN, TypeID: "example", Parameters: map[string]any{},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return onboarding.SourceRelease{
		ID: releaseID, Digest: strings.Repeat("d", 64), MaterializationDigest: strings.Repeat("e", 64), CreatedAt: now,
		Offers: offers,
		Sources: []onboarding.ReleaseSource{{
			SourceID: sourceID, MetadataVersion: "1.0", MetadataPayloadDigest: strings.Repeat("a", 64),
			DeploymentDigest: strings.Repeat("b", 64), CheckedAt: now, ExpiresAt: activation.ExpiresAt,
			FreshUntil: activation.FreshUntil, StaleUntil: activation.StaleUntil, Snapshot: snapshotBody,
			CertificateSet: onboarding.PublicCertificateSet{ID: sourceID, Certificates: []onboarding.PublicCertificate{{
				Role: "issuer", Subject: "CN=test", SHA256: strings.Repeat("c", 64), NotAfter: now.AddDate(1, 0, 0),
			}}},
			TypeMetadata: []onboarding.TypeMetadata{{
				VCT: activation.Types[0].VCT, Version: "1.0", Integrity: integrity,
				MediaType: "application/json", Bytes: metadataBody,
			}},
		}},
	}
}
