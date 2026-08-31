package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

func TestActivationFromRegistryCandidateAcceptsAdditiveSnapshotFields(t *testing.T) {
	now := time.Now().UTC()
	release := runtimeTestRelease(t, "release", "belastingdienst", "offer", now)
	source := release.Sources[0]
	var snapshot map[string]any
	if err := json.Unmarshal(source.Snapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["future_additive_field"] = map[string]any{"enabled": true}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	candidate := onboarding.SourceCandidate{
		SourceID: source.SourceID, MetadataVersion: source.MetadataVersion,
		MetadataPayloadDigest: source.MetadataPayloadDigest, DeploymentDigest: source.DeploymentDigest,
		CheckedAt: source.CheckedAt, ExpiresAt: source.ExpiresAt, FreshUntil: source.FreshUntil, StaleUntil: source.StaleUntil,
		Snapshot: body, CertificateSet: source.CertificateSet, TypeMetadata: source.TypeMetadata,
	}
	if _, err := activationFromRegistryCandidate(candidate); err != nil {
		t.Fatalf("additive snapshot field broke rolling-upgrade compatibility: %v", err)
	}
}

type wrappedMissingCertificateStore struct{}

func (wrappedMissingCertificateStore) Load(sourceRegistration) (certificateArtifacts, error) {
	return certificateArtifacts{}, fmt.Errorf("load certificate: %w", fs.ErrNotExist)
}

func TestCertificateStoreAdapterPreservesMissingFileClassification(t *testing.T) {
	_, err := (certificateStoreAdapter{store: wrappedMissingCertificateStore{}}).Load(context.Background(), onboarding.Source{ID: "missing"})
	if !errors.Is(err, onboarding.ErrCertificateNotFound) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing certificate classification was not preserved: %v", err)
	}
	if strings.Contains(err.Error(), "%!") {
		t.Fatalf("malformed wrapped error: %v", err)
	}
}
