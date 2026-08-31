package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	onboardingcore "gbo-demo/eudi-adapter/internal/onboarding"
)

type sourceActivation struct {
	SchemaVersion              string                              `json:"schema_version"`
	Source                     sourceRegistration                  `json:"source"`
	MetadataURL                string                              `json:"metadata_url"`
	MetadataVersion            string                              `json:"metadata_version"`
	MetadataPayloadDigest      string                              `json:"metadata_payload_digest"`
	MetadataETag               string                              `json:"metadata_etag,omitempty"`
	CheckedAt                  time.Time                           `json:"checked_at,omitempty"`
	ExpiresAt                  time.Time                           `json:"expires_at"`
	FreshUntil                 time.Time                           `json:"fresh_until"`
	StaleUntil                 time.Time                           `json:"stale_until"`
	TransportAuthenticated     bool                                `json:"transport_authenticated"`
	TypeMetadataStoreReference string                              `json:"type_metadata_store_reference"`
	Types                      []activatedType                     `json:"types"`
	Certificates               certificateArtifacts                `json:"certificates"`
	RegistryDeploymentDigest   string                              `json:"-"`
	PublicCertificates         onboardingcore.PublicCertificateSet `json:"-"`
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
	sourceID              string
	sourceOIN             string
	sourceName            string
	sourceLogoPath        string
	certificateStoreName  string
	readerPublicURL       string
	secretsDir            string
	publicCertificatesDir string
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
	case "public-filesystem":
		return newPublicDevelopmentCertificateStore(options.secretsDir, options.readerPublicURL), nil
	default:
		return nil, fmt.Errorf("unsupported certificate store %q", options.certificateStoreName)
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
	artifacts, err := provider.Provision(registration)
	if err != nil {
		return true, fmt.Errorf("provision development certificates: %w", err)
	}
	if err := projectPublicCertificateArtifacts(options.publicCertificatesDir, registration.SourceID, artifacts); err != nil {
		return true, fmt.Errorf("project public development certificates: %w", err)
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
	set.StringVar(&options.secretsDir, "secrets-dir", ".local/secrets", "filesystem secret directory")
	set.StringVar(&options.publicCertificatesDir, "public-certificates-dir", getEnv("ONBOARDING_PUBLIC_CERTIFICATES_DIR", ".local/public-certificates"), "public-only certificate projection directory")
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

func activationDeploymentDigest(activation *sourceActivation) (string, error) {
	if activation == nil {
		return "", fmt.Errorf("source activation is required")
	}
	if activation.RegistryDeploymentDigest != "" {
		return activation.RegistryDeploymentDigest, nil
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
