package onboarding

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memoryRegistry struct {
	mu          sync.RWMutex
	candidates  map[string]SourceCandidate
	statuses    map[string]Status
	releases    map[string]SourceRelease
	active      string
	failPromote error
}

func newMemoryRegistry() *memoryRegistry {
	return &memoryRegistry{
		candidates: make(map[string]SourceCandidate), statuses: make(map[string]Status), releases: make(map[string]SourceRelease),
	}
}

func (m *memoryRegistry) Candidate(_ context.Context, sourceID string) (SourceCandidate, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	candidate, found := m.candidates[sourceID]
	return candidate, found, nil
}

func (m *memoryRegistry) PutCandidate(_ context.Context, candidate SourceCandidate) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.candidates[candidate.SourceID] = candidate
	return nil
}

func (m *memoryRegistry) PutStatus(_ context.Context, status Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[status.SourceID] = status
	return nil
}

func (m *memoryRegistry) Promote(_ context.Context, release SourceRelease) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPromote != nil {
		return false, m.failPromote
	}
	if _, exists := m.releases[release.ID]; exists && m.active != "" && m.active != release.ID {
		return false, nil
	}
	m.releases[release.ID] = release
	m.active = release.ID
	return true, nil
}

func (m *memoryRegistry) ActiveReleaseState(context.Context) (ActiveReleaseState, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.active == "" {
		return ActiveReleaseState{}, false, nil
	}
	release := m.releases[m.active]
	state := ActiveReleaseState{ReleaseID: release.ID, Sources: make([]ReleaseSourceLifecycle, len(release.Sources))}
	for index, source := range release.Sources {
		state.Sources[index] = ReleaseSourceLifecycle{
			SourceID: source.SourceID, CheckedAt: source.CheckedAt, ExpiresAt: source.ExpiresAt,
			FreshUntil: source.FreshUntil, StaleUntil: source.StaleUntil,
		}
	}
	return state, true, nil
}

func (m *memoryRegistry) ActiveRelease(context.Context) (SourceRelease, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.active == "" {
		return SourceRelease{}, ErrNoActiveRelease
	}
	return m.releases[m.active], nil
}

func (m *memoryRegistry) Release(_ context.Context, releaseID string) (SourceRelease, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	release, found := m.releases[releaseID]
	if !found {
		return SourceRelease{}, ErrReleaseNotFound
	}
	return release, nil
}

func (m *memoryRegistry) ActivateRelease(_ context.Context, releaseID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, found := m.releases[releaseID]; !found {
		return ErrReleaseNotFound
	}
	m.active = releaseID
	return nil
}

func TestMemoryRegistryCandidateStatusPromotionFailureAndRollback(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	registry := newMemoryRegistry()
	candidate := registryTestCandidate("belastingdienst", "offer-v1", now)
	if err := registry.PutCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if err := registry.PutStatus(ctx, Status{SourceID: candidate.SourceID, State: StateRolloutRequired, CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	first, err := NewSourceRelease(now, []SourceCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Promote(ctx, first); err != nil {
		t.Fatal(err)
	}

	changed := candidate
	changed.MetadataVersion = "2.0"
	changed.Offers = []byte(`[{"key":"offer-v2"}]`)
	second, err := NewSourceRelease(now.Add(time.Minute), []SourceCandidate{changed})
	if err != nil {
		t.Fatal(err)
	}
	registry.failPromote = errors.New("injected transaction failure")
	if _, err := registry.Promote(ctx, second); err == nil {
		t.Fatal("promotion unexpectedly succeeded")
	}
	active, err := registry.ActiveRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != first.ID {
		t.Fatalf("failed promotion changed active release to %q", active.ID)
	}

	registry.failPromote = nil
	if _, err := registry.Promote(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := registry.ActivateRelease(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	active, err = registry.ActiveRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != first.ID {
		t.Fatalf("rollback active release = %q, want %q", active.ID, first.ID)
	}
}

var _ SourceRegistry = (*memoryRegistry)(nil)
