package onboarding

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSourceReleaseDigestIsDeterministic(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	one := registryTestCandidate("rvig", "rvig-offer", now)
	two := registryTestCandidate("belastingdienst", "belastingdienst-offer", now)

	first, err := NewSourceRelease(now, []SourceCandidate{one, two})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSourceRelease(now.Add(time.Hour), []SourceCandidate{two, one})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || first.ID != second.ID || first.MaterializationDigest != second.MaterializationDigest {
		t.Fatalf("release digests differ for identical inputs: first=%+v second=%+v", first, second)
	}
	if first.Sources[0].SourceID != "belastingdienst" {
		t.Fatalf("sources are not canonical: %+v", first.Sources)
	}
}

func TestFreshnessChangesReleaseDigestButNotMaterializationDigest(t *testing.T) {
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	firstCandidate := registryTestCandidate("belastingdienst", "offer", now)
	secondCandidate := firstCandidate
	secondCandidate.CheckedAt = now.Add(time.Minute)
	secondCandidate.FreshUntil = secondCandidate.FreshUntil.Add(time.Minute)
	secondCandidate.StaleUntil = secondCandidate.StaleUntil.Add(time.Minute)

	first, err := NewSourceRelease(now, []SourceCandidate{firstCandidate})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSourceRelease(now.Add(time.Minute), []SourceCandidate{secondCandidate})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == second.Digest {
		t.Fatal("freshness-only release must receive a distinct full digest")
	}
	if first.MaterializationDigest != second.MaterializationDigest {
		t.Fatal("freshness-only release unexpectedly changed the materialization digest")
	}
}

func TestSourceCandidateRejectsPrivateKeyMaterial(t *testing.T) {
	candidate := registryTestCandidate("belastingdienst", "offer", time.Now().UTC())
	candidate.Snapshot = json.RawMessage(`{"issuer_key_reference":"/secret/issuer.key"}`)
	if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "key_reference") {
		t.Fatalf("Validate() error = %v, want secret-bearing field rejection", err)
	}

	candidate.Snapshot = json.RawMessage(`{"value":"-----BEGIN PRIVATE KEY-----"}`)
	if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "private key material") {
		t.Fatalf("Validate() error = %v, want private key material rejection", err)
	}
}

func TestSourceReleaseRejectsDuplicateOfferKeys(t *testing.T) {
	now := time.Now().UTC()
	_, err := NewSourceRelease(now, []SourceCandidate{
		registryTestCandidate("belastingdienst", "shared", now),
		registryTestCandidate("rvig", "shared", now),
	})
	if err == nil || !strings.Contains(err.Error(), "not globally unique") {
		t.Fatalf("NewSourceRelease() error = %v, want duplicate offer failure", err)
	}
}

func TestSourceReleaseValidationRejectsMutationAfterDigest(t *testing.T) {
	now := time.Now().UTC()
	release, err := NewSourceRelease(now, []SourceCandidate{registryTestCandidate("belastingdienst", "offer", now)})
	if err != nil {
		t.Fatal(err)
	}
	release.Sources[0].MetadataVersion = "2.0"
	if err := release.Validate(); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("Validate() error = %v, want immutable-content digest failure", err)
	}
}

func registryTestCandidate(sourceID, offerKey string, now time.Time) SourceCandidate {
	digest := strings.Repeat("a", 64)
	metadataBytes := []byte(`{"name":"example"}`)
	metadataDigest := sha256.Sum256(metadataBytes)
	certificates := make([]PublicCertificate, 0, 5)
	for _, role := range []string{"issuer", "reader", "status", "issuer_ca", "reader_ca"} {
		certificates = append(certificates, PublicCertificate{
			Role: role, Subject: "CN=" + sourceID + " " + role, SHA256: strings.Repeat("b", 64), NotAfter: now.AddDate(1, 0, 0),
		})
	}
	return SourceCandidate{
		SourceID: sourceID, MetadataVersion: "1.0", MetadataPayloadDigest: digest,
		DeploymentDigest: digest, CheckedAt: now, ExpiresAt: now.Add(2 * time.Hour),
		FreshUntil: now.Add(15 * time.Minute), StaleUntil: now.Add(time.Hour),
		TransportAuthenticated: true,
		Snapshot:               json.RawMessage(`{"source":{"source_id":"` + sourceID + `"}}`),
		Offers:                 json.RawMessage(`[{"key":"` + offerKey + `"}]`),
		CertificateSet:         PublicCertificateSet{ID: sourceID, Certificates: certificates},
		TypeMetadata: []TypeMetadata{{
			VCT: "https://example.test/types/" + sourceID + "/example/v1.0", Version: "1.0",
			Integrity: "sha256-" + base64.StdEncoding.EncodeToString(metadataDigest[:]), MediaType: "application/json", Bytes: metadataBytes,
		}},
	}
}
