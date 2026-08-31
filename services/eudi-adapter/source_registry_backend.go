package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
)

type registryActivationBackend struct {
	ctx      context.Context
	registry onboarding.SourceRegistry
}

type registryStatusWriter struct {
	ctx      context.Context
	registry onboarding.SourceRegistry
}

func (w registryStatusWriter) Write(status sourceReconcileStatus) error {
	return w.registry.PutStatus(w.ctx, onboarding.Status{
		SourceID: status.SourceID, State: onboarding.State(status.State), Reason: onboarding.Reason(status.Reason),
		Message: status.Message, MetadataVersion: status.MetadataVersion, DeploymentDigest: status.DeploymentDigest,
		TransportAuthenticated: status.TransportAuthenticated, CheckedAt: status.CheckedAt,
	})
}

type registryActivationSnapshot struct {
	SchemaVersion          string                  `json:"schema_version"`
	Source                 sourceRegistration      `json:"source"`
	MetadataURL            string                  `json:"metadata_url"`
	MetadataVersion        string                  `json:"metadata_version"`
	MetadataPayloadDigest  string                  `json:"metadata_payload_digest"`
	MetadataETag           string                  `json:"metadata_etag,omitempty"`
	CheckedAt              time.Time               `json:"checked_at"`
	ExpiresAt              time.Time               `json:"expires_at"`
	FreshUntil             time.Time               `json:"fresh_until"`
	StaleUntil             time.Time               `json:"stale_until"`
	TransportAuthenticated bool                    `json:"transport_authenticated"`
	Types                  []registryActivatedType `json:"types"`
}

type registryActivatedType struct {
	TypeID       string                      `json:"type_id"`
	TypeVersion  string                      `json:"type_version"`
	VCT          string                      `json:"vct"`
	VCTIntegrity string                      `json:"vct_integrity"`
	Offers       []sourceOffer               `json:"offers"`
	Definition   sourceAttestationDefinition `json:"definition"`
}

func newRegistryActivationBackend(ctx context.Context, registry onboarding.SourceRegistry) *registryActivationBackend {
	return &registryActivationBackend{ctx: ctx, registry: registry}
}

func (b *registryActivationBackend) CurrentCandidate(sourceID string) (*sourceActivation, error) {
	if b == nil || b.registry == nil {
		return nil, fmt.Errorf("source registry is required")
	}
	candidate, found, err := b.registry.Candidate(b.ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	return activationFromRegistryCandidate(candidate)
}

func (b *registryActivationBackend) Activate(validated *validatedSourceRegistration, certificates certificateArtifacts) (*sourceActivation, error) {
	if validated == nil || !sourceIDPattern.MatchString(validated.Registration.SourceID) {
		return nil, fmt.Errorf("activation requires a valid source_id")
	}
	activation := activationFromValidatedSource(validated)
	certificateSet, err := publicCertificateSet(validated.Registration.certificateSetID(), certificates)
	if err != nil {
		return nil, err
	}
	typeMetadata := make([]onboarding.TypeMetadata, 0, len(validated.Publications))
	for _, publication := range validated.Publications {
		typeMetadata = append(typeMetadata, onboarding.TypeMetadata{
			VCT: publication.VCT, Version: publication.TypeVersion, Integrity: publication.Integrity,
			MediaType: "application/json", Bytes: append([]byte(nil), publication.body...),
		})
	}
	candidate, err := registryCandidateFromActivation(activation, certificateSet, typeMetadata)
	if err != nil {
		return nil, err
	}
	if existing, found, err := b.registry.Candidate(b.ctx, candidate.SourceID); err != nil {
		return nil, err
	} else if found {
		comparison, err := compareNumericVersion(candidate.MetadataVersion, existing.MetadataVersion)
		if err != nil {
			return nil, fmt.Errorf("compare candidate versions: %w", err)
		}
		if comparison < 0 {
			return nil, fmt.Errorf("metadata version rollback from %q to %q is not allowed", existing.MetadataVersion, candidate.MetadataVersion)
		}
		if comparison == 0 && existing.MetadataPayloadDigest != candidate.MetadataPayloadDigest {
			return nil, fmt.Errorf("metadata version %q has a different metadata payload", candidate.MetadataVersion)
		}
	}
	if err := b.registry.PutCandidate(b.ctx, candidate); err != nil {
		return nil, err
	}
	activation.RegistryDeploymentDigest = candidate.DeploymentDigest
	activation.PublicCertificates = certificateSet
	return activation, nil
}

func (b *registryActivationBackend) RefreshCandidate(sourceID string, source sourceRegistration, metadataURL string, certificates certificateArtifacts, transportAuthenticated bool, now time.Time) (*sourceActivation, error) {
	candidate, found, err := b.registry.Candidate(b.ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, os.ErrNotExist
	}
	activation, err := activationFromRegistryCandidate(candidate)
	if err != nil {
		return nil, err
	}
	if source.SourceID != sourceID {
		return nil, fmt.Errorf("refreshed source registration belongs to %q", source.SourceID)
	}
	if err := source.validate(); err != nil {
		return nil, fmt.Errorf("validate refreshed source registration: %w", err)
	}
	if activation.ExpiresAt.Sub(now) < defaultSourceMetadataCachePolicy.MinimumValidity {
		return nil, fmt.Errorf("source metadata validity is shorter than the GBO minimum")
	}
	certificateSet, err := publicCertificateSet(source.certificateSetID(), certificates)
	if err != nil {
		return nil, err
	}
	activation.Source = source
	activation.MetadataURL = metadataURL
	activation.TransportAuthenticated = transportAuthenticated
	activation.CheckedAt = now.UTC()
	activation.FreshUntil = minTime(now.Add(defaultSourceMetadataCachePolicy.MaximumFreshness), activation.ExpiresAt)
	activation.StaleUntil = minTime(activation.FreshUntil.Add(defaultSourceMetadataCachePolicy.StaleGrace), activation.ExpiresAt)
	refreshed, err := registryCandidateFromActivation(activation, certificateSet, candidate.TypeMetadata)
	if err != nil {
		return nil, err
	}
	if err := b.registry.PutCandidate(b.ctx, refreshed); err != nil {
		return nil, err
	}
	activation.RegistryDeploymentDigest = refreshed.DeploymentDigest
	activation.PublicCertificates = certificateSet
	return activation, nil
}

func (b *registryActivationBackend) RolloutRequired(candidate *sourceActivation) (bool, error) {
	if candidate == nil {
		return false, fmt.Errorf("source activation is required")
	}
	digest, err := activationDeploymentDigest(candidate)
	if err != nil {
		return false, err
	}
	release, err := b.registry.ActiveRelease(b.ctx)
	if errors.Is(err, onboarding.ErrNoActiveRelease) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	for _, source := range release.Sources {
		if source.SourceID == candidate.Source.SourceID {
			return source.DeploymentDigest != digest, nil
		}
	}
	return true, nil
}

func promoteRegistryCandidates(ctx context.Context, registry onboarding.SourceRegistry, sources []sourceConfiguration, at time.Time) (onboarding.SourceRelease, error) {
	if len(sources) == 0 {
		return onboarding.SourceRelease{}, fmt.Errorf("configured source set is empty")
	}
	candidates := make([]onboarding.SourceCandidate, 0, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if _, duplicate := seen[source.SourceID]; duplicate {
			return onboarding.SourceRelease{}, fmt.Errorf("configured source_id %q is duplicated", source.SourceID)
		}
		seen[source.SourceID] = struct{}{}
		candidate, found, err := registry.Candidate(ctx, source.SourceID)
		if err != nil {
			return onboarding.SourceRelease{}, err
		}
		if !found {
			return onboarding.SourceRelease{}, fmt.Errorf("configured source %q has no complete candidate", source.SourceID)
		}
		if at.After(candidate.StaleUntil) {
			return onboarding.SourceRelease{}, fmt.Errorf("configured source %q is outside stale grace", source.SourceID)
		}
		candidates = append(candidates, candidate)
	}
	release, err := onboarding.NewSourceRelease(at, candidates)
	if err != nil {
		return onboarding.SourceRelease{}, err
	}
	if err := registry.Promote(ctx, release); err != nil {
		return onboarding.SourceRelease{}, err
	}
	for _, candidate := range candidates {
		if err := registry.PutStatus(ctx, onboarding.Status{
			SourceID: candidate.SourceID, State: onboarding.StateActive,
			MetadataVersion: candidate.MetadataVersion, DeploymentDigest: candidate.DeploymentDigest,
			TransportAuthenticated: candidate.TransportAuthenticated, CheckedAt: at.UTC(),
		}); err != nil {
			return release, fmt.Errorf("mark promoted source %q active: %w", candidate.SourceID, err)
		}
	}
	return release, nil
}

func activationFromValidatedSource(validated *validatedSourceRegistration) *sourceActivation {
	types := make([]activatedType, 0, len(validated.Publications))
	for _, publication := range validated.Publications {
		var offers []sourceOffer
		var definition sourceAttestationDefinition
		for _, candidate := range validated.Document.eudiAttestations() {
			if candidate.TypeID == publication.TypeID {
				offers = append([]sourceOffer(nil), candidate.Offers...)
				definition = candidate
				break
			}
		}
		types = append(types, activatedType{
			TypeID: publication.TypeID, TypeVersion: publication.TypeVersion,
			VCT: publication.VCT, VCTIntegrity: publication.Integrity,
			Offers: offers, Definition: definition,
		})
	}
	payloadDigest := sha256.Sum256(validated.Payload)
	return &sourceActivation{
		SchemaVersion: "2.0", Source: validated.Registration, MetadataURL: validated.MetadataURL,
		MetadataVersion: validated.Document.Version, MetadataPayloadDigest: hex.EncodeToString(payloadDigest[:]),
		MetadataETag: validated.MetadataETag, ExpiresAt: validated.ExpiresAt,
		CheckedAt:              validated.ValidatedAt.UTC(),
		FreshUntil:             minTime(validated.ValidatedAt.Add(defaultSourceMetadataCachePolicy.MaximumFreshness), validated.ExpiresAt),
		StaleUntil:             minTime(validated.ValidatedAt.Add(defaultSourceMetadataCachePolicy.MaximumFreshness+defaultSourceMetadataCachePolicy.StaleGrace), validated.ExpiresAt),
		TransportAuthenticated: validated.Registration.MetadataEndpoint.Transport == sourceTransportFSC,
		Types:                  types,
	}
}

func registryCandidateFromActivation(activation *sourceActivation, certificateSet onboarding.PublicCertificateSet, typeMetadata []onboarding.TypeMetadata) (onboarding.SourceCandidate, error) {
	snapshot, err := registrySnapshotFromActivation(activation)
	if err != nil {
		return onboarding.SourceCandidate{}, err
	}
	snapshotBody, err := json.Marshal(snapshot)
	if err != nil {
		return onboarding.SourceCandidate{}, fmt.Errorf("marshal registry source snapshot: %w", err)
	}
	offers := make([]publicIssuanceOffer, 0)
	for _, activatedType := range activation.Types {
		for _, offer := range activatedType.Offers {
			offers = append(offers, publicIssuanceOffer{
				Key: offer.ID, Label: offer.Label, Description: offer.Description,
				AttestationType: activatedType.VCT, SourceID: activation.Source.SourceID,
				SourceOIN: activation.Source.SourceOIN, TypeID: activatedType.TypeID, Parameters: offer.Parameters,
			})
		}
	}
	sort.Slice(offers, func(i, j int) bool { return offers[i].Key < offers[j].Key })
	offersBody, err := json.Marshal(offers)
	if err != nil {
		return onboarding.SourceCandidate{}, fmt.Errorf("marshal source offers: %w", err)
	}
	material, err := json.Marshal(struct {
		Snapshot     json.RawMessage                 `json:"snapshot"`
		Certificates onboarding.PublicCertificateSet `json:"certificates"`
		TypeMetadata []onboarding.TypeMetadata       `json:"type_metadata"`
		Offers       json.RawMessage                 `json:"offers"`
	}{Snapshot: snapshotBody, Certificates: certificateSet, TypeMetadata: typeMetadata, Offers: offersBody})
	if err != nil {
		return onboarding.SourceCandidate{}, fmt.Errorf("marshal source deployment material: %w", err)
	}
	materialDigest := sha256.Sum256(material)
	return onboarding.SourceCandidate{
		SourceID: activation.Source.SourceID, MetadataVersion: activation.MetadataVersion,
		MetadataPayloadDigest: activation.MetadataPayloadDigest, MetadataETag: activation.MetadataETag,
		DeploymentDigest: hex.EncodeToString(materialDigest[:]), CheckedAt: activation.CheckedAt,
		ExpiresAt: activation.ExpiresAt, FreshUntil: activation.FreshUntil, StaleUntil: activation.StaleUntil,
		TransportAuthenticated: activation.TransportAuthenticated, Snapshot: snapshotBody, Offers: offersBody,
		CertificateSet: certificateSet, TypeMetadata: append([]onboarding.TypeMetadata(nil), typeMetadata...),
	}, nil
}

func registrySnapshotFromActivation(activation *sourceActivation) (registryActivationSnapshot, error) {
	if activation == nil {
		return registryActivationSnapshot{}, fmt.Errorf("source activation is required")
	}
	types := make([]registryActivatedType, len(activation.Types))
	for index, activatedType := range activation.Types {
		types[index] = registryActivatedType{
			TypeID: activatedType.TypeID, TypeVersion: activatedType.TypeVersion,
			VCT: activatedType.VCT, VCTIntegrity: activatedType.VCTIntegrity,
			Offers: append([]sourceOffer(nil), activatedType.Offers...), Definition: activatedType.Definition,
		}
	}
	return registryActivationSnapshot{
		SchemaVersion: "2.0", Source: activation.Source, MetadataURL: activation.MetadataURL,
		MetadataVersion: activation.MetadataVersion, MetadataPayloadDigest: activation.MetadataPayloadDigest,
		MetadataETag: activation.MetadataETag, ExpiresAt: activation.ExpiresAt,
		CheckedAt:  activation.CheckedAt,
		FreshUntil: activation.FreshUntil, StaleUntil: activation.StaleUntil,
		TransportAuthenticated: activation.TransportAuthenticated, Types: types,
	}, nil
}

func activationFromRegistryCandidate(candidate onboarding.SourceCandidate) (*sourceActivation, error) {
	var snapshot registryActivationSnapshot
	decoder := json.NewDecoder(strings.NewReader(string(candidate.Snapshot)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode registry source snapshot: %w", err)
	}
	if snapshot.SchemaVersion != "2.0" || snapshot.Source.SourceID != candidate.SourceID {
		return nil, fmt.Errorf("registry source snapshot is inconsistent")
	}
	types := make([]activatedType, len(snapshot.Types))
	for index, stored := range snapshot.Types {
		types[index] = activatedType{
			TypeID: stored.TypeID, TypeVersion: stored.TypeVersion, VCT: stored.VCT,
			VCTIntegrity: stored.VCTIntegrity, TypeMetadataReference: stored.VCT,
			Offers: append([]sourceOffer(nil), stored.Offers...), Definition: stored.Definition,
		}
	}
	return &sourceActivation{
		SchemaVersion: snapshot.SchemaVersion, Source: snapshot.Source, MetadataURL: snapshot.MetadataURL,
		MetadataVersion: snapshot.MetadataVersion, MetadataPayloadDigest: snapshot.MetadataPayloadDigest,
		MetadataETag: snapshot.MetadataETag, ExpiresAt: snapshot.ExpiresAt,
		CheckedAt:  snapshot.CheckedAt,
		FreshUntil: snapshot.FreshUntil, StaleUntil: snapshot.StaleUntil,
		TransportAuthenticated: snapshot.TransportAuthenticated, Types: types,
		RegistryDeploymentDigest: candidate.DeploymentDigest, PublicCertificates: candidate.CertificateSet,
	}, nil
}

func publicCertificateSet(id string, artifacts certificateArtifacts) (onboarding.PublicCertificateSet, error) {
	paths := []struct {
		role string
		path string
		pem  bool
	}{
		{"issuer", artifacts.IssuerCertReference, false},
		{"reader", artifacts.ReaderCertReference, false},
		{"status", artifacts.StatusCertReference, false},
		{"issuer_ca", artifacts.IssuerCACertReference, true},
		{"reader_ca", artifacts.ReaderCACertReference, true},
	}
	result := onboarding.PublicCertificateSet{ID: id, Certificates: make([]onboarding.PublicCertificate, 0, len(paths))}
	for _, item := range paths {
		if item.path == "" {
			return onboarding.PublicCertificateSet{}, fmt.Errorf("public %s certificate reference is missing", item.role)
		}
		body, err := os.ReadFile(item.path)
		if err != nil {
			return onboarding.PublicCertificateSet{}, fmt.Errorf("read public %s certificate: %w", item.role, err)
		}
		var der []byte
		if item.pem {
			block, rest := pem.Decode(body)
			if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
				return onboarding.PublicCertificateSet{}, fmt.Errorf("public %s certificate must contain exactly one PEM certificate", item.role)
			}
			der = block.Bytes
		} else {
			der, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(body)))
			if err != nil {
				return onboarding.PublicCertificateSet{}, fmt.Errorf("decode public %s certificate: %w", item.role, err)
			}
		}
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return onboarding.PublicCertificateSet{}, fmt.Errorf("parse public %s certificate: %w", item.role, err)
		}
		digest := sha256.Sum256(certificate.Raw)
		var ekuOIDs []string
		if len(certificate.UnknownExtKeyUsage) > 0 {
			ekuOIDs = make([]string, 0, len(certificate.UnknownExtKeyUsage))
		}
		for _, oid := range certificate.UnknownExtKeyUsage {
			ekuOIDs = append(ekuOIDs, oid.String())
		}
		result.Certificates = append(result.Certificates, onboarding.PublicCertificate{
			Role: item.role, Subject: certificate.Subject.String(), SHA256: hex.EncodeToString(digest[:]),
			NotAfter: certificate.NotAfter.UTC(), DNSNames: append([]string(nil), certificate.DNSNames...), EKUOIDs: ekuOIDs,
		})
	}
	return result, nil
}
