package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

type sourceActivation struct {
	SchemaVersion              string               `json:"schema_version"`
	Source                     sourceRegistration   `json:"source"`
	MetadataURL                string               `json:"metadata_url"`
	MetadataVersion            string               `json:"metadata_version"`
	MetadataPayloadDigest      string               `json:"metadata_payload_digest"`
	MetadataETag               string               `json:"metadata_etag,omitempty"`
	ExpiresAt                  time.Time            `json:"expires_at"`
	FreshUntil                 time.Time            `json:"fresh_until"`
	StaleUntil                 time.Time            `json:"stale_until"`
	TransportAuthenticated     bool                 `json:"transport_authenticated"`
	TypeMetadataStoreReference string               `json:"type_metadata_store_reference"`
	Types                      []activatedType      `json:"types"`
	Certificates               certificateArtifacts `json:"certificates"`
}

type activatedType struct {
	TypeID                string                      `json:"type_id"`
	TypeVersion           string                      `json:"type_version"`
	VCT                   string                      `json:"vct"`
	VCTIntegrity          string                      `json:"vct_integrity"`
	TypeMetadataReference string                      `json:"type_metadata_reference"`
	Offers                []sourceOffer               `json:"offers"`
	Definition            sourceAttestationDefinition `json:"definition"`
}

func (t activatedType) validate() error {
	if !typeIDPattern.MatchString(t.TypeID) {
		return fmt.Errorf("type_id has an invalid format")
	}
	if !numericVersionPattern.MatchString(t.TypeVersion) {
		return fmt.Errorf("type_version has an invalid format")
	}
	if t.VCT == "" || t.TypeMetadataReference == "" || len(t.Offers) == 0 {
		return fmt.Errorf("attestation type is incomplete")
	}
	if !validSHA256Integrity(t.VCTIntegrity) {
		return fmt.Errorf("vct_integrity must be a SHA-256 integrity value")
	}
	if t.Definition.TypeID != t.TypeID || t.Definition.TypeVersion != t.TypeVersion {
		return fmt.Errorf("definition does not match activated type and version")
	}
	if err := validateSourceAttestation(t.Definition); err != nil {
		return fmt.Errorf("definition: %w", err)
	}
	if !reflect.DeepEqual(t.Definition.Offers, t.Offers) {
		return fmt.Errorf("definition offers do not match activated offers")
	}
	return nil
}

func validSHA256Integrity(value string) bool {
	const prefix = "sha256-"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	digest, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && len(digest) == sha256.Size
}

type onboardingOptions struct {
	sourceID             string
	sourceOIN            string
	sourceName           string
	sourceLogoPath       string
	storageBackend       string
	certificateStoreName string
	readerPublicURL      string
	stateDir             string
	secretsDir           string
}

type onboardingDependencies struct {
	resolveCertificateProvider func(onboardingOptions) (certificateProvider, error)
	stdout                     io.Writer
	stderr                     io.Writer
}

type activationBackend interface {
	Activate(*validatedSourceRegistration, certificateArtifacts) (*sourceActivation, error)
}

type activationLifecycleBackend interface {
	CurrentCandidate(sourceID string) (*sourceActivation, error)
	RefreshCandidate(sourceID string, source sourceRegistration, metadataURL string, certificates certificateArtifacts, transportAuthenticated bool, now time.Time) (*sourceActivation, error)
	RolloutRequired(*sourceActivation) (bool, error)
}

func defaultOnboardingDependencies() onboardingDependencies {
	return onboardingDependencies{
		resolveCertificateProvider: configuredCertificateProvider,
		stdout:                     os.Stdout,
		stderr:                     os.Stderr,
	}
}

func configuredCertificateProvider(options onboardingOptions) (certificateProvider, error) {
	return newDevelopmentCAProvider(options.secretsDir, options.readerPublicURL), nil
}

func configuredCertificateStore(options onboardingOptions) (certificateStore, error) {
	switch options.certificateStoreName {
	case "filesystem":
		return newDevelopmentCAProvider(options.secretsDir, options.readerPublicURL), nil
	default:
		return nil, fmt.Errorf("unsupported certificate store %q", options.certificateStoreName)
	}
}

func configuredActivationBackend(options onboardingOptions) (activationBackend, error) {
	switch options.storageBackend {
	case "filesystem":
		return newFilesystemActivationBackend(options.stateDir), nil
	default:
		return nil, fmt.Errorf("unsupported onboarding storage backend %q", options.storageBackend)
	}
}

func runOnboardingCommand(_ context.Context, arguments []string, dependencies onboardingDependencies) (bool, error) {
	if len(arguments) == 0 || arguments[0] != "provision-development-certificates" {
		return false, nil
	}
	command := arguments[0]
	options, err := parseOnboardingOptions(command, arguments[1:], dependencies.stderr)
	if err != nil {
		return true, err
	}
	registration := sourceRegistration{SourceID: options.sourceID, SourceOIN: options.sourceOIN, Name: options.sourceName}
	if registration.Name == "" {
		registration.Name = "Source " + registration.SourceOIN
	}
	if options.sourceLogoPath != "" {
		logo, err := loadOrganizationLogo(options.sourceLogoPath)
		if err != nil {
			return true, err
		}
		registration.Logo = logo
	}
	provider, err := dependencies.resolveCertificateProvider(options)
	if err != nil {
		return true, err
	}
	if _, err := provider.Provision(registration); err != nil {
		return true, fmt.Errorf("provision development certificates: %w", err)
	}
	_, _ = fmt.Fprintf(dependencies.stdout, "development certificates provisioned for source %s; this command is for local development only\n", registration.SourceOIN)
	return true, nil
}

func parseOnboardingOptions(command string, arguments []string, errorOutput io.Writer) (onboardingOptions, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(errorOutput)
	options := onboardingOptions{}
	set.StringVar(&options.sourceID, "source-id", "", "stable source identifier and certificate directory name")
	set.StringVar(&options.sourceOIN, "source-oin", "", "20-digit OIN to bind the local development certificates to")
	set.StringVar(&options.sourceName, "source-name", "", "source name for the certificate subject (defaults to a generic OIN-based name)")
	set.StringVar(&options.sourceLogoPath, "source-logo", "", "SVG, PNG or JPEG logo to embed in the certificate authorization metadata")
	set.StringVar(&options.readerPublicURL, "reader-public-url", os.Getenv("EUDI_PUBLIC_URL"), "public issuance-server URL whose host becomes the reader certificate DNS SAN")
	set.StringVar(&options.stateDir, "state-dir", ".local/onboarding", "filesystem onboarding state directory")
	set.StringVar(&options.secretsDir, "secrets-dir", ".local/secrets", "filesystem secret directory")
	if err := set.Parse(arguments); err != nil {
		return onboardingOptions{}, err
	}
	if set.NArg() != 0 {
		return onboardingOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	if !sourceIDPattern.MatchString(options.sourceID) {
		return onboardingOptions{}, fmt.Errorf("--source-id is invalid")
	}
	if !sourceOINPattern.MatchString(options.sourceOIN) {
		return onboardingOptions{}, fmt.Errorf("--source-oin must contain exactly 20 digits")
	}
	return options, nil
}

type filesystemActivationBackend struct {
	stateDir string
}

func newFilesystemActivationBackend(stateDir string) *filesystemActivationBackend {
	return &filesystemActivationBackend{stateDir: stateDir}
}

func (b *filesystemActivationBackend) CurrentCandidate(sourceID string) (*sourceActivation, error) {
	if !sourceIDPattern.MatchString(sourceID) {
		return nil, fmt.Errorf("candidate source_id is invalid")
	}
	return loadSourceActivation(filepath.Join(b.stateDir, "candidates", sourceID+".json"))
}

func (b *filesystemActivationBackend) RefreshCandidate(sourceID string, source sourceRegistration, metadataURL string, certificates certificateArtifacts, transportAuthenticated bool, now time.Time) (*sourceActivation, error) {
	activation, err := b.CurrentCandidate(sourceID)
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
	activation.Source = source
	activation.MetadataURL = metadataURL
	activation.Certificates = certificates
	activation.TransportAuthenticated = transportAuthenticated
	activation.FreshUntil = minTime(now.Add(defaultSourceMetadataCachePolicy.MaximumFreshness), activation.ExpiresAt)
	activation.StaleUntil = minTime(activation.FreshUntil.Add(defaultSourceMetadataCachePolicy.StaleGrace), activation.ExpiresAt)
	if err := b.writeCandidateAndMatchingActive(activation); err != nil {
		return nil, err
	}
	return activation, nil
}

func (b *filesystemActivationBackend) RolloutRequired(candidate *sourceActivation) (bool, error) {
	deployed, err := loadSourceActivation(filepath.Join(b.stateDir, "active", candidate.Source.SourceID+".json"))
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	candidateDigest, err := activationDeploymentDigest(candidate)
	if err != nil {
		return false, err
	}
	deployedDigest, err := activationDeploymentDigest(deployed)
	if err != nil {
		return false, err
	}
	return candidateDigest != deployedDigest, nil
}

func (b *filesystemActivationBackend) writeCandidateAndMatchingActive(activation *sourceActivation) error {
	body, err := json.MarshalIndent(activation, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal source activation: %w", err)
	}
	body = append(body, '\n')
	if err := writeFileAtomically(filepath.Join(b.stateDir, "candidates"), activation.Source.SourceID+".json", body, 0o644); err != nil {
		return err
	}
	rolloutRequired, err := b.RolloutRequired(activation)
	if err != nil {
		return err
	}
	if !rolloutRequired {
		if err := writeFileAtomically(filepath.Join(b.stateDir, "active"), activation.Source.SourceID+".json", body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (b *filesystemActivationBackend) Activate(validated *validatedSourceRegistration, certificates certificateArtifacts) (*sourceActivation, error) {
	if validated == nil || !sourceIDPattern.MatchString(validated.Registration.SourceID) {
		return nil, fmt.Errorf("activation requires a valid source_id")
	}
	typeStore := filepath.Join(b.stateDir, "type-metadata")
	candidateStore := filepath.Join(b.stateDir, "candidates")
	activeStore := filepath.Join(b.stateDir, "active")
	for _, directory := range []string{typeStore, candidateStore, activeStore} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("create onboarding state directory %q: %w", directory, err)
		}
	}
	for _, publication := range validated.Publications {
		if err := persistTypeMetadataPublication(typeStore, publication); err != nil {
			return nil, err
		}
	}
	types := make([]activatedType, 0, len(validated.Publications))
	for _, publication := range validated.Publications {
		var offers []sourceOffer
		var activatedDefinition sourceAttestationDefinition
		for _, definition := range validated.Document.eudiAttestations() {
			if definition.TypeID == publication.TypeID {
				offers = append([]sourceOffer(nil), definition.Offers...)
				activatedDefinition = definition
				break
			}
		}
		types = append(types, activatedType{
			TypeID:                publication.TypeID,
			TypeVersion:           publication.TypeVersion,
			VCT:                   publication.VCT,
			VCTIntegrity:          publication.Integrity,
			TypeMetadataReference: filepath.Join(typeStore, typeMetadataFilename(publication)),
			Offers:                offers,
			Definition:            activatedDefinition,
		})
	}
	payloadDigest := sha256.Sum256(validated.Payload)
	activation := &sourceActivation{
		SchemaVersion:              "1.0",
		Source:                     validated.Registration,
		MetadataURL:                validated.MetadataURL,
		MetadataVersion:            validated.Document.Version,
		MetadataPayloadDigest:      hex.EncodeToString(payloadDigest[:]),
		MetadataETag:               validated.MetadataETag,
		ExpiresAt:                  validated.ExpiresAt,
		FreshUntil:                 minTime(validated.ValidatedAt.Add(defaultSourceMetadataCachePolicy.MaximumFreshness), validated.ExpiresAt),
		StaleUntil:                 minTime(validated.ValidatedAt.Add(defaultSourceMetadataCachePolicy.MaximumFreshness+defaultSourceMetadataCachePolicy.StaleGrace), validated.ExpiresAt),
		TransportAuthenticated:     validated.Registration.MetadataEndpoint.Transport == sourceTransportFSC,
		TypeMetadataStoreReference: typeStore,
		Types:                      types,
		Certificates:               certificates,
	}
	activationBytes, err := json.MarshalIndent(activation, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal active source registration: %w", err)
	}
	activationBytes = append(activationBytes, '\n')
	activationPath := filepath.Join(candidateStore, validated.Registration.SourceID+".json")
	if err := writeSourceActivation(activationPath, activationBytes, activation); err != nil {
		return nil, fmt.Errorf("activate source registration: %w", err)
	}
	if deployed, err := loadSourceActivation(filepath.Join(activeStore, validated.Registration.SourceID+".json")); err == nil {
		deployedDigest, digestErr := activationDeploymentDigest(deployed)
		candidateDigest, candidateErr := activationDeploymentDigest(activation)
		if digestErr != nil || candidateErr != nil {
			return nil, errors.Join(digestErr, candidateErr)
		}
		if deployedDigest == candidateDigest {
			if err := writeFileAtomically(activeStore, validated.Registration.SourceID+".json", activationBytes, 0o644); err != nil {
				return nil, fmt.Errorf("refresh deployed source registration: %w", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read deployed source registration: %w", err)
	}
	return activation, nil
}

func loadSourceActivation(path string) (*sourceActivation, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var activation sourceActivation
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&activation); err != nil {
		return nil, fmt.Errorf("parse source activation: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("parse source activation: trailing JSON data")
	}
	return &activation, nil
}

func activationDeploymentDigest(activation *sourceActivation) (string, error) {
	if activation == nil {
		return "", fmt.Errorf("source activation is required")
	}
	stable := struct {
		SourceID     string               `json:"source_id"`
		SourceOIN    string               `json:"source_oin"`
		Name         string               `json:"name"`
		Types        []activatedType      `json:"types"`
		Certificates certificateArtifacts `json:"certificates"`
	}{
		SourceID: activation.Source.SourceID, SourceOIN: activation.Source.SourceOIN,
		Name: activation.Source.Name, Types: activation.Types, Certificates: activation.Certificates,
	}
	body, err := json.Marshal(stable)
	if err != nil {
		return "", fmt.Errorf("marshal source deployment snapshot: %w", err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

func writeSourceActivation(path string, body []byte, next *sourceActivation) error {
	existingBody, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeFileAtomically(filepath.Dir(path), filepath.Base(path), body, 0o644)
	}
	if err != nil {
		return err
	}
	if bytes.Equal(existingBody, body) {
		return nil
	}
	var existing sourceActivation
	if err := json.Unmarshal(existingBody, &existing); err != nil {
		return fmt.Errorf("parse existing activation: %w", err)
	}
	if existing.Source.SourceID != next.Source.SourceID {
		return fmt.Errorf("existing activation belongs to source %q", existing.Source.SourceID)
	}
	comparison, err := compareNumericVersion(next.MetadataVersion, existing.MetadataVersion)
	if err != nil {
		return fmt.Errorf("compare activation versions: %w", err)
	}
	if comparison < 0 {
		return fmt.Errorf("metadata version rollback from %q to %q is not allowed", existing.MetadataVersion, next.MetadataVersion)
	}
	if comparison == 0 && existing.MetadataPayloadDigest != next.MetadataPayloadDigest {
		return fmt.Errorf("metadata version %q has a different metadata payload", next.MetadataVersion)
	}
	return writeFileAtomically(filepath.Dir(path), filepath.Base(path), body, 0o644)
}
