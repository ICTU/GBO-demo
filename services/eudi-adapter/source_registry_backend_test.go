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

	"gbo-demo/source-registry/onboarding"
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

// candidateRegistry keeps candidates in memory; every other registry call
// panics through the nil embedded interface.
type candidateRegistry struct {
	onboarding.SourceRegistry
	candidates map[string]onboarding.SourceCandidate
}

func (r *candidateRegistry) Candidate(_ context.Context, sourceID string) (onboarding.SourceCandidate, bool, error) {
	candidate, found := r.candidates[sourceID]
	return candidate, found, nil
}

func (r *candidateRegistry) PutCandidate(_ context.Context, candidate onboarding.SourceCandidate) error {
	r.candidates[candidate.SourceID] = candidate
	return nil
}

// A source may not reuse a metadata version for different content, nor go
// back to a version lower than the last validated one (issue #610).
func TestActivateEnforcesTheMetadataVersionRule(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	registration := sourceRegistration{
		SourceID: "demo", SourceOIN: "99999999900000000200", Name: "Demo",
		MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportUnsecured, Endpoint: "https://source.example/metadata"},
		DataAccess:       sourceDataAccess{Transport: sourceTransportUnsecured},
	}
	secrets := t.TempDir()
	writeTestDevelopmentCAs(t, secrets, now)
	provider := newDevelopmentCAProvider(secrets, "https://issuer.example")
	provider.now = func() time.Time { return now }
	certificates, err := provider.Provision(registration)
	if err != nil {
		t.Fatal(err)
	}
	validated := func(t *testing.T, version, offerLabel string) *validatedSourceRegistration {
		t.Helper()
		document := unsecuredDocument(t)
		document["version"] = version
		offer := eudiOf(document)["attestations"].([]any)[0].(map[string]any)["offers"].([]any)[0].(map[string]any)
		offer["label"] = offerLabel
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte(strings.ReplaceAll(string(raw), sourceURLPlaceholder, "https://source.example"))
		result, err := validateSourcePayload(registration, payload, registration.MetadataEndpoint.Endpoint, testSchemaPath, "https://issuer.example", now)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	tests := []struct {
		name       string
		version    string
		offerLabel string
		rejection  string
	}{
		{name: "same version, same content", version: "2.4.0", offerLabel: "Inkomensverklaring 2024"},
		{name: "same version, different content", version: "2.4.0", offerLabel: "Inkomensverklaring 2024 (gewijzigd)", rejection: "different metadata payload"},
		{name: "lower version", version: "2.3.9", offerLabel: "Inkomensverklaring 2024", rejection: "rollback"},
		{name: "higher version", version: "2.5.0", offerLabel: "Inkomensverklaring 2024 (gewijzigd)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := &candidateRegistry{candidates: map[string]onboarding.SourceCandidate{}}
			backend := newRegistryActivationBackend(context.Background(), registry)
			if _, err := backend.Activate(validated(t, "2.4.0", "Inkomensverklaring 2024"), certificates); err != nil {
				t.Fatalf("activate the last validated metadata: %v", err)
			}
			last := registry.candidates["demo"]

			activation, err := backend.Activate(validated(t, test.version, test.offerLabel), certificates)
			stored := registry.candidates["demo"]
			if test.rejection == "" {
				if err != nil {
					t.Fatalf("Activate error = %v, want activated", err)
				}
				if stored.MetadataVersion != test.version || stored.MetadataPayloadDigest != activation.MetadataPayloadDigest {
					t.Fatalf("stored candidate = version %q digest %q, want the activated metadata", stored.MetadataVersion, stored.MetadataPayloadDigest)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.rejection) {
				t.Fatalf("Activate error = %v, want a rejection naming %q", err, test.rejection)
			}
			if activation != nil {
				t.Fatal("rejected metadata returned an activation")
			}
			if stored.MetadataVersion != last.MetadataVersion || stored.MetadataPayloadDigest != last.MetadataPayloadDigest || stored.DeploymentDigest != last.DeploymentDigest {
				t.Fatalf("rejected metadata replaced the stored candidate: version %q, want %q", stored.MetadataVersion, last.MetadataVersion)
			}
		})
	}
}
