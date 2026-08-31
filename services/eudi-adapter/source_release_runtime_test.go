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

func (f *runtimeRegistryFake) ActiveReleaseState(context.Context) (onboarding.ActiveReleaseState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return onboarding.ActiveReleaseState{}, false, f.err
	}
	if f.active == "" {
		return onboarding.ActiveReleaseState{}, false, nil
	}
	return runtimeReleaseState(f.releases[f.active]), true, nil
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
	if loaded.releaseID != first.ID {
		t.Fatalf("due refresh blocked request or replaced snapshot before completion: %+v", loaded)
	}
	loaded = waitForRuntimeRelease(t, runtime, second.ID)
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
	waitForRuntimeRefresh(t, runtime)
	if runtime.lastErr == nil {
		t.Fatal("registry failure was not recorded for retry")
	}
}

func TestSourceReleaseRuntimeRefreshesLifecycleWithoutRebuildingRelease(t *testing.T) {
	now := time.Now().UTC()
	release := runtimeTestRelease(t, "release-one", "belastingdienst", "offer-one", now)
	registry := &runtimeRegistryFake{active: release.ID, releases: map[string]onboarding.SourceRelease{release.ID: release}}
	runtime := &sourceReleaseRuntime{registry: registry, baseConfig: config{}, refreshInterval: time.Second}
	first, err := runtime.current(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}

	refreshed := release
	refreshed.Sources = append([]onboarding.ReleaseSource(nil), release.Sources...)
	refreshed.Sources[0].CheckedAt = refreshed.Sources[0].CheckedAt.Add(30 * time.Second)
	refreshed.Sources[0].FreshUntil = refreshed.Sources[0].FreshUntil.Add(30 * time.Second)
	refreshed.Sources[0].StaleUntil = refreshed.Sources[0].StaleUntil.Add(30 * time.Second)
	registry.mu.Lock()
	registry.releases[release.ID] = refreshed
	registry.mu.Unlock()
	served, err := runtime.current(context.Background(), now.Add(2*time.Second))
	if err != nil || served != first {
		t.Fatalf("lifecycle refresh blocked the request: snapshot=%p err=%v", served, err)
	}
	waitForRuntimeRefresh(t, runtime)
	runtime.mu.Lock()
	updated := runtime.snapshot
	runtime.mu.Unlock()
	if updated == first || !updated.staleUntil.Equal(refreshed.Sources[0].StaleUntil) {
		t.Fatalf("runtime lifecycle was not atomically refreshed: %+v", updated)
	}
	binding := updated.bindings["belastingdienst\x00example"]
	sourceRuntime := binding.runtime.(*releaseSourceRuntime)
	if !sourceRuntime.freshUntil.Equal(refreshed.Sources[0].FreshUntil) {
		t.Fatalf("source freshness = %s, want %s", sourceRuntime.freshUntil, refreshed.Sources[0].FreshUntil)
	}
}

type blockingRuntimeRegistry struct {
	*runtimeRegistryFake
	started chan struct{}
	release chan struct{}
}

func (f *blockingRuntimeRegistry) ActiveReleaseState(ctx context.Context) (onboarding.ActiveReleaseState, bool, error) {
	select {
	case <-f.started:
	default:
		close(f.started)
	}
	select {
	case <-f.release:
		return f.runtimeRegistryFake.ActiveReleaseState(ctx)
	case <-ctx.Done():
		return onboarding.ActiveReleaseState{}, false, ctx.Err()
	}
}

func TestSourceReleaseRuntimeDoesNotSerializeRequestsBehindRefresh(t *testing.T) {
	now := time.Now().UTC()
	release := runtimeTestRelease(t, "release-one", "belastingdienst", "offer-one", now)
	base := &runtimeRegistryFake{active: release.ID, releases: map[string]onboarding.SourceRelease{release.ID: release}}
	runtime := &sourceReleaseRuntime{registry: base, baseConfig: config{}, refreshInterval: time.Second}
	first, err := runtime.current(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}

	blocking := &blockingRuntimeRegistry{runtimeRegistryFake: base, started: make(chan struct{}), release: make(chan struct{})}
	runtime.registry = blocking
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	returned := make(chan *releaseRuntimeSnapshot, 1)
	go func() {
		snapshot, _ := runtime.current(requestCtx, now.Add(2*time.Second))
		returned <- snapshot
	}()
	select {
	case snapshot := <-returned:
		if snapshot != first {
			t.Fatal("request did not retain the previous snapshot during refresh")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("request blocked behind Source Registry refresh")
	}
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}
	close(blocking.release)
	waitForRuntimeRefresh(t, runtime)
}

type racingRuntimeRegistry struct {
	pointer onboarding.SourceRelease
	loaded  onboarding.SourceRelease
}

func (f racingRuntimeRegistry) ActiveReleaseState(context.Context) (onboarding.ActiveReleaseState, bool, error) {
	return runtimeReleaseState(f.pointer), true, nil
}

func runtimeReleaseState(release onboarding.SourceRelease) onboarding.ActiveReleaseState {
	state := onboarding.ActiveReleaseState{ReleaseID: release.ID, Sources: make([]onboarding.ReleaseSourceLifecycle, len(release.Sources))}
	for index, source := range release.Sources {
		state.Sources[index] = onboarding.ReleaseSourceLifecycle{
			SourceID: source.SourceID, CheckedAt: source.CheckedAt, ExpiresAt: source.ExpiresAt,
			FreshUntil: source.FreshUntil, StaleUntil: source.StaleUntil,
		}
	}
	return state
}

func (f racingRuntimeRegistry) ActiveRelease(context.Context) (onboarding.SourceRelease, error) {
	return f.loaded, nil
}

func TestSourceReleaseRuntimeRetainsSnapshotWhenActivePointerChangesDuringRead(t *testing.T) {
	now := time.Now().UTC()
	first := runtimeTestRelease(t, "release-one", "belastingdienst", "offer-one", now)
	second := runtimeTestRelease(t, "release-two", "rvig", "offer-two", now.Add(time.Minute))
	runtime := &sourceReleaseRuntime{
		registry:   &runtimeRegistryFake{active: first.ID, releases: map[string]onboarding.SourceRelease{first.ID: first}},
		baseConfig: config{}, refreshInterval: time.Second,
	}
	loaded, err := runtime.current(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	runtime.registry = racingRuntimeRegistry{pointer: second, loaded: first}
	retained, err := runtime.current(context.Background(), now.Add(2*time.Second))
	if err != nil || retained != loaded {
		t.Fatalf("pointer race did not retain snapshot: retained=%p err=%v", retained, err)
	}
	waitForRuntimeRefresh(t, runtime)
	if runtime.lastErr == nil || !strings.Contains(runtime.lastErr.Error(), "changed while loading") {
		t.Fatalf("pointer race was not recorded for retry: %v", runtime.lastErr)
	}
}

func waitForRuntimeRelease(t *testing.T, runtime *sourceReleaseRuntime, releaseID string) *releaseRuntimeSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		snapshot, refreshing := runtime.snapshot, runtime.refreshing
		runtime.mu.Unlock()
		if !refreshing && snapshot != nil && snapshot.releaseID == releaseID {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("runtime did not activate release %q", releaseID)
	return nil
}

func waitForRuntimeRefresh(t *testing.T, runtime *sourceReleaseRuntime) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		runtime.mu.Lock()
		refreshing := runtime.refreshing
		runtime.mu.Unlock()
		if !refreshing {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime refresh did not complete")
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
