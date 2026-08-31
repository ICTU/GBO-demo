package onboarding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrNoActiveRelease = errors.New("source registry has no active release")
	ErrReleaseNotFound = errors.New("source release not found")
)

// PublicCertificate describes certificate material that may safely cross the
// registry boundary. Secret paths and private key material deliberately have
// no representation in this model.
type PublicCertificate struct {
	Role     string    `json:"role"`
	Subject  string    `json:"subject"`
	SHA256   string    `json:"sha256"`
	NotAfter time.Time `json:"not_after"`
	DNSNames []string  `json:"dns_names,omitempty"`
	EKUOIDs  []string  `json:"eku_oids,omitempty"`
}

type PublicCertificateSet struct {
	ID           string              `json:"id"`
	Certificates []PublicCertificate `json:"certificates"`
}

type TypeMetadata struct {
	VCT       string `json:"vct"`
	Version   string `json:"version"`
	Integrity string `json:"integrity"`
	MediaType string `json:"media_type"`
	Bytes     []byte `json:"bytes"`
}

// SourceCandidate is mutable control-plane state. Snapshot and Offers are
// versioned domain JSON; lifecycle fields remain typed and queryable.
type SourceCandidate struct {
	SourceID               string               `json:"source_id"`
	MetadataVersion        string               `json:"metadata_version"`
	MetadataPayloadDigest  string               `json:"metadata_payload_digest"`
	MetadataETag           string               `json:"metadata_etag,omitempty"`
	DeploymentDigest       string               `json:"deployment_digest"`
	CheckedAt              time.Time            `json:"checked_at"`
	ExpiresAt              time.Time            `json:"expires_at"`
	FreshUntil             time.Time            `json:"fresh_until"`
	StaleUntil             time.Time            `json:"stale_until"`
	TransportAuthenticated bool                 `json:"transport_authenticated"`
	Snapshot               json.RawMessage      `json:"snapshot"`
	Offers                 json.RawMessage      `json:"offers"`
	CertificateSet         PublicCertificateSet `json:"certificate_set"`
	TypeMetadata           []TypeMetadata       `json:"type_metadata"`
}

type ReleaseSource struct {
	SourceID               string               `json:"source_id"`
	MetadataVersion        string               `json:"metadata_version"`
	MetadataPayloadDigest  string               `json:"metadata_payload_digest"`
	MetadataETag           string               `json:"metadata_etag,omitempty"`
	DeploymentDigest       string               `json:"deployment_digest"`
	CheckedAt              time.Time            `json:"checked_at"`
	ExpiresAt              time.Time            `json:"expires_at"`
	FreshUntil             time.Time            `json:"fresh_until"`
	StaleUntil             time.Time            `json:"stale_until"`
	TransportAuthenticated bool                 `json:"transport_authenticated"`
	Snapshot               json.RawMessage      `json:"snapshot"`
	CertificateSet         PublicCertificateSet `json:"certificate_set"`
	TypeMetadata           []TypeMetadata       `json:"type_metadata"`
}

type SourceRelease struct {
	ID                    string          `json:"id"`
	Digest                string          `json:"digest"`
	MaterializationDigest string          `json:"materialization_digest"`
	CreatedAt             time.Time       `json:"created_at"`
	Sources               []ReleaseSource `json:"sources"`
	Offers                json.RawMessage `json:"offers"`
}

// SourceRegistry is shaped around onboarding use cases rather than database
// tables. Promotion and activation are required to be atomic operations.
type SourceRegistry interface {
	Candidate(context.Context, string) (SourceCandidate, bool, error)
	PutCandidate(context.Context, SourceCandidate) error
	PutStatus(context.Context, Status) error
	Promote(context.Context, SourceRelease) error
	ActiveReleaseID(context.Context) (string, bool, error)
	ActiveRelease(context.Context) (SourceRelease, error)
	Release(context.Context, string) (SourceRelease, error)
	ActivateRelease(context.Context, string) error
}

func (release SourceRelease) Validate() error {
	if release.ID == "" || release.ID != release.Digest || !validHexDigest(release.Digest) ||
		!validHexDigest(release.MaterializationDigest) || release.CreatedAt.IsZero() || len(release.Sources) == 0 {
		return fmt.Errorf("source release is incomplete")
	}
	if !json.Valid(release.Offers) {
		return fmt.Errorf("source release offers must be valid JSON")
	}
	if err := rejectSecretJSON(release.Offers); err != nil {
		return fmt.Errorf("source release offers: %w", err)
	}
	seen := make(map[string]struct{}, len(release.Sources))
	for _, source := range release.Sources {
		if _, duplicate := seen[source.SourceID]; duplicate {
			return fmt.Errorf("duplicate release source_id %q", source.SourceID)
		}
		seen[source.SourceID] = struct{}{}
		candidate := SourceCandidate{
			SourceID: source.SourceID, MetadataVersion: source.MetadataVersion,
			MetadataPayloadDigest: source.MetadataPayloadDigest, MetadataETag: source.MetadataETag,
			DeploymentDigest: source.DeploymentDigest, CheckedAt: source.CheckedAt,
			ExpiresAt: source.ExpiresAt, FreshUntil: source.FreshUntil, StaleUntil: source.StaleUntil,
			TransportAuthenticated: source.TransportAuthenticated, Snapshot: source.Snapshot,
			Offers: json.RawMessage(`[]`), CertificateSet: source.CertificateSet, TypeMetadata: source.TypeMetadata,
		}
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("release source %q: %w", source.SourceID, err)
		}
	}
	full, err := releaseDigestDocument(release, true)
	if err != nil {
		return err
	}
	material, err := releaseDigestDocument(release, false)
	if err != nil {
		return err
	}
	if digestHex(full) != release.Digest || digestHex(material) != release.MaterializationDigest {
		return fmt.Errorf("source release digest does not match its immutable contents")
	}
	return nil
}

func NewSourceRelease(createdAt time.Time, candidates []SourceCandidate) (SourceRelease, error) {
	if len(candidates) == 0 {
		return SourceRelease{}, fmt.Errorf("source release requires at least one candidate")
	}
	ordered := append([]SourceCandidate(nil), candidates...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SourceID < ordered[j].SourceID })

	seenSources := make(map[string]struct{}, len(ordered))
	seenOffers := make(map[string]struct{})
	publicOffers := make([]json.RawMessage, 0)
	sources := make([]ReleaseSource, 0, len(ordered))
	for _, candidate := range ordered {
		if err := candidate.Validate(); err != nil {
			return SourceRelease{}, fmt.Errorf("candidate %q: %w", candidate.SourceID, err)
		}
		if _, duplicate := seenSources[candidate.SourceID]; duplicate {
			return SourceRelease{}, fmt.Errorf("duplicate candidate source_id %q", candidate.SourceID)
		}
		seenSources[candidate.SourceID] = struct{}{}
		var offers []json.RawMessage
		if err := json.Unmarshal(candidate.Offers, &offers); err != nil {
			return SourceRelease{}, fmt.Errorf("candidate %q offers: %w", candidate.SourceID, err)
		}
		for _, offer := range offers {
			var identity struct {
				Key string `json:"key"`
			}
			if err := json.Unmarshal(offer, &identity); err != nil || strings.TrimSpace(identity.Key) == "" {
				return SourceRelease{}, fmt.Errorf("candidate %q contains an offer without a key", candidate.SourceID)
			}
			if _, duplicate := seenOffers[identity.Key]; duplicate {
				return SourceRelease{}, fmt.Errorf("issuance offer key %q is not globally unique", identity.Key)
			}
			seenOffers[identity.Key] = struct{}{}
			publicOffers = append(publicOffers, append(json.RawMessage(nil), offer...))
		}
		typeMetadata := append([]TypeMetadata(nil), candidate.TypeMetadata...)
		sort.Slice(typeMetadata, func(i, j int) bool {
			if typeMetadata[i].VCT == typeMetadata[j].VCT {
				return typeMetadata[i].Version < typeMetadata[j].Version
			}
			return typeMetadata[i].VCT < typeMetadata[j].VCT
		})
		sources = append(sources, ReleaseSource{
			SourceID: candidate.SourceID, MetadataVersion: candidate.MetadataVersion,
			MetadataPayloadDigest: candidate.MetadataPayloadDigest, MetadataETag: candidate.MetadataETag,
			DeploymentDigest: candidate.DeploymentDigest, CheckedAt: candidate.CheckedAt.UTC(), ExpiresAt: candidate.ExpiresAt.UTC(),
			FreshUntil: candidate.FreshUntil.UTC(), StaleUntil: candidate.StaleUntil.UTC(),
			TransportAuthenticated: candidate.TransportAuthenticated,
			Snapshot:               append(json.RawMessage(nil), candidate.Snapshot...), CertificateSet: candidate.CertificateSet,
			TypeMetadata: typeMetadata,
		})
	}
	sort.Slice(publicOffers, func(i, j int) bool { return bytes.Compare(publicOffers[i], publicOffers[j]) < 0 })
	offers, err := json.Marshal(publicOffers)
	if err != nil {
		return SourceRelease{}, fmt.Errorf("marshal release offers: %w", err)
	}

	release := SourceRelease{CreatedAt: createdAt.UTC(), Sources: sources, Offers: offers}
	full, err := releaseDigestDocument(release, true)
	if err != nil {
		return SourceRelease{}, err
	}
	material, err := releaseDigestDocument(release, false)
	if err != nil {
		return SourceRelease{}, err
	}
	release.Digest = digestHex(full)
	release.ID = release.Digest
	release.MaterializationDigest = digestHex(material)
	if err := release.Validate(); err != nil {
		return SourceRelease{}, err
	}
	return release, nil
}

func (c SourceCandidate) Validate() error {
	if strings.TrimSpace(c.SourceID) == "" || strings.TrimSpace(c.MetadataVersion) == "" {
		return fmt.Errorf("source_id and metadata_version are required")
	}
	for name, value := range map[string]string{
		"metadata_payload_digest": c.MetadataPayloadDigest,
		"deployment_digest":       c.DeploymentDigest,
	} {
		if !validHexDigest(value) {
			return fmt.Errorf("%s must be a SHA-256 hex digest", name)
		}
	}
	if c.CheckedAt.IsZero() || c.ExpiresAt.IsZero() || c.FreshUntil.IsZero() || c.StaleUntil.IsZero() {
		return fmt.Errorf("candidate lifecycle timestamps are required")
	}
	if c.FreshUntil.After(c.StaleUntil) || c.StaleUntil.After(c.ExpiresAt) {
		return fmt.Errorf("candidate freshness must satisfy fresh_until <= stale_until <= expires_at")
	}
	if !json.Valid(c.Snapshot) || !json.Valid(c.Offers) {
		return fmt.Errorf("candidate snapshot and offers must be valid JSON")
	}
	if err := rejectSecretJSON(c.Snapshot); err != nil {
		return fmt.Errorf("candidate snapshot: %w", err)
	}
	if strings.TrimSpace(c.CertificateSet.ID) == "" || len(c.CertificateSet.Certificates) == 0 {
		return fmt.Errorf("public certificate set is required")
	}
	seenRoles := make(map[string]struct{}, len(c.CertificateSet.Certificates))
	requiredRoles := map[string]struct{}{"issuer": {}, "reader": {}, "status": {}, "issuer_ca": {}, "reader_ca": {}}
	for _, certificate := range c.CertificateSet.Certificates {
		if certificate.Role == "" || certificate.Subject == "" || !validHexDigest(certificate.SHA256) || certificate.NotAfter.IsZero() {
			return fmt.Errorf("public certificate descriptor is incomplete")
		}
		if _, duplicate := seenRoles[certificate.Role]; duplicate {
			return fmt.Errorf("duplicate public certificate role %q", certificate.Role)
		}
		seenRoles[certificate.Role] = struct{}{}
		delete(requiredRoles, certificate.Role)
	}
	if len(requiredRoles) != 0 {
		missing := make([]string, 0, len(requiredRoles))
		for role := range requiredRoles {
			missing = append(missing, role)
		}
		sort.Strings(missing)
		return fmt.Errorf("public certificate set is missing roles: %s", strings.Join(missing, ", "))
	}
	if len(c.TypeMetadata) == 0 {
		return fmt.Errorf("candidate requires Type Metadata")
	}
	for _, metadata := range c.TypeMetadata {
		if metadata.VCT == "" || metadata.Version == "" || metadata.Integrity == "" || len(metadata.Bytes) == 0 {
			return fmt.Errorf("Type Metadata is incomplete")
		}
		digest := sha256.Sum256(metadata.Bytes)
		integrity := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
		if metadata.Integrity != integrity {
			return fmt.Errorf("Type Metadata integrity mismatch for %q", metadata.VCT)
		}
	}
	return nil
}

func releaseDigestDocument(release SourceRelease, includeLifecycle bool) ([]byte, error) {
	type digestSource struct {
		SourceID               string               `json:"source_id"`
		MetadataVersion        string               `json:"metadata_version"`
		MetadataPayloadDigest  string               `json:"metadata_payload_digest"`
		MetadataETag           string               `json:"metadata_etag,omitempty"`
		DeploymentDigest       string               `json:"deployment_digest"`
		CheckedAt              time.Time            `json:"checked_at,omitempty"`
		ExpiresAt              time.Time            `json:"expires_at,omitempty"`
		FreshUntil             time.Time            `json:"fresh_until,omitempty"`
		StaleUntil             time.Time            `json:"stale_until,omitempty"`
		TransportAuthenticated bool                 `json:"transport_authenticated"`
		Snapshot               json.RawMessage      `json:"snapshot"`
		CertificateSet         PublicCertificateSet `json:"certificate_set"`
		TypeMetadata           []TypeMetadata       `json:"type_metadata"`
	}
	sources := make([]digestSource, len(release.Sources))
	for index, source := range release.Sources {
		sources[index] = digestSource{
			SourceID: source.SourceID, MetadataVersion: source.MetadataVersion,
			MetadataPayloadDigest: source.MetadataPayloadDigest, DeploymentDigest: source.DeploymentDigest,
			TransportAuthenticated: source.TransportAuthenticated, Snapshot: source.Snapshot,
			CertificateSet: source.CertificateSet, TypeMetadata: source.TypeMetadata,
		}
		if includeLifecycle {
			sources[index].MetadataETag = source.MetadataETag
			sources[index].CheckedAt = source.CheckedAt
			sources[index].ExpiresAt = source.ExpiresAt
			sources[index].FreshUntil = source.FreshUntil
			sources[index].StaleUntil = source.StaleUntil
		}
	}
	return json.Marshal(struct {
		Sources []digestSource  `json:"sources"`
		Offers  json.RawMessage `json:"offers"`
	}{Sources: sources, Offers: release.Offers})
}

func rejectSecretJSON(raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var walk func(any) error
	walk = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, nested := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
				if strings.Contains(normalized, "private_key") || strings.HasSuffix(normalized, "key_reference") {
					return fmt.Errorf("secret-bearing field %q is not allowed", key)
				}
				if err := walk(nested); err != nil {
					return err
				}
			}
		case []any:
			for _, nested := range typed {
				if err := walk(nested); err != nil {
					return err
				}
			}
		case string:
			if strings.Contains(typed, "-----BEGIN PRIVATE KEY-----") {
				return fmt.Errorf("private key material is not allowed")
			}
		}
		return nil
	}
	return walk(value)
}

func validHexDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func digestHex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
