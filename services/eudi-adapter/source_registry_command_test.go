package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

type releaseOperationsFake struct {
	releases map[string]onboarding.SourceRelease
	active   string
	err      error
}

func (f *releaseOperationsFake) ActiveRelease(context.Context) (onboarding.SourceRelease, error) {
	if f.err != nil {
		return onboarding.SourceRelease{}, f.err
	}
	return f.releases[f.active], nil
}

func (f *releaseOperationsFake) Release(_ context.Context, releaseID string) (onboarding.SourceRelease, error) {
	release, found := f.releases[releaseID]
	if !found {
		return onboarding.SourceRelease{}, onboarding.ErrReleaseNotFound
	}
	return release, nil
}

func (f *releaseOperationsFake) ActivateRelease(_ context.Context, releaseID string) error {
	if f.err != nil {
		return f.err
	}
	if _, found := f.releases[releaseID]; !found {
		return onboarding.ErrReleaseNotFound
	}
	f.active = releaseID
	return nil
}

func TestActivateSourceReleaseRollsBackPointerAfterTargetValidation(t *testing.T) {
	first := onboarding.SourceRelease{ID: strings.Repeat("a", 64)}
	second := onboarding.SourceRelease{ID: strings.Repeat("b", 64)}
	registry := &releaseOperationsFake{
		releases: map[string]onboarding.SourceRelease{first.ID: first, second.ID: second}, active: second.ID,
	}
	active, err := activateSourceRelease(context.Background(), registry, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != first.ID || registry.active != first.ID {
		t.Fatalf("active release = %q, want %q", active.ID, first.ID)
	}
	if _, err := activateSourceRelease(context.Background(), registry, "missing"); !errors.Is(err, onboarding.ErrReleaseNotFound) {
		t.Fatalf("missing target error = %v, want ErrReleaseNotFound", err)
	}
	if registry.active != first.ID {
		t.Fatal("missing rollback target changed the active release")
	}
}

func TestSourceReleaseSummaryContainsNoSnapshotsOffersOrCertificates(t *testing.T) {
	now := time.Now().UTC()
	release := onboarding.SourceRelease{
		ID: strings.Repeat("a", 64), Digest: strings.Repeat("a", 64),
		MaterializationDigest: strings.Repeat("b", 64), CreatedAt: now,
		Offers: json.RawMessage(`[{"key":"secret-looking-offer"}]`),
		Sources: []onboarding.ReleaseSource{{
			SourceID: "belastingdienst", MetadataVersion: "1.0", DeploymentDigest: strings.Repeat("c", 64),
			Snapshot:               json.RawMessage(`{"private_key":"must-not-print"}`),
			CertificateSet:         onboarding.PublicCertificateSet{ID: "must-not-print"},
			TransportAuthenticated: true, FreshUntil: now.Add(time.Hour), StaleUntil: now.Add(2 * time.Hour),
		}},
	}
	var output bytes.Buffer
	if err := writeSourceReleaseSummary(&output, release); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private_key", "must-not-print", "secret-looking-offer", "certificate_set", "snapshot", "offers"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("summary contains forbidden %q: %s", forbidden, output.String())
		}
	}
	if !strings.Contains(output.String(), release.ID) || !strings.Contains(output.String(), "belastingdienst") {
		t.Fatalf("summary omits release identity: %s", output.String())
	}
}

var _ sourceReleaseOperations = (*releaseOperationsFake)(nil)
