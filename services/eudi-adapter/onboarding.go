package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type sourceActivation struct {
	SchemaVersion              string               `json:"schema_version"`
	Source                     sourceRegistration   `json:"source"`
	MetadataURL                string               `json:"metadata_url"`
	MetadataVersion            string               `json:"metadata_version"`
	MetadataPayloadDigest      string               `json:"metadata_payload_digest"`
	TypeMetadataStoreReference string               `json:"type_metadata_store_reference"`
	Types                      []activatedType      `json:"types"`
	Certificates               certificateArtifacts `json:"certificates"`
}

type activatedType struct {
	TypeID                string        `json:"type_id"`
	TypeVersion           string        `json:"type_version"`
	VCT                   string        `json:"vct"`
	VCTIntegrity          string        `json:"vct_integrity"`
	TypeMetadataReference string        `json:"type_metadata_reference"`
	Offers                []sourceOffer `json:"offers"`
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
	sourcePath           string
	sourceOIN            string
	sourceName           string
	storageBackend       string
	certificateStoreName string
	dryRun               bool
	outwayURL            string
	schemaPath           string
	publicBaseURL        string
	readerPublicURL      string
	stateDir             string
	secretsDir           string
}

type onboardingDependencies struct {
	client                     *http.Client
	now                        func() time.Time
	resolveCertificateProvider func(onboardingOptions) (certificateProvider, error)
	resolveCertificateStore    func(onboardingOptions) (certificateStore, error)
	resolveActivationBackend   func(onboardingOptions) (activationBackend, error)
	stdout                     io.Writer
	stderr                     io.Writer
}

type activationBackend interface {
	Activate(*validatedSourceRegistration, certificateArtifacts) (*sourceActivation, error)
}

func defaultOnboardingDependencies() onboardingDependencies {
	return onboardingDependencies{
		client:                     &http.Client{Timeout: 15 * time.Second},
		now:                        time.Now,
		resolveCertificateProvider: configuredCertificateProvider,
		resolveCertificateStore:    configuredCertificateStore,
		resolveActivationBackend:   configuredActivationBackend,
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

func runOnboardingCommand(ctx context.Context, arguments []string, dependencies onboardingDependencies) (bool, error) {
	if len(arguments) == 0 || (arguments[0] != "validate-source" && arguments[0] != "onboard-source" && arguments[0] != "provision-development-certificates") {
		return false, nil
	}
	command := arguments[0]
	options, err := parseOnboardingOptions(command, arguments[1:], dependencies.stderr)
	if err != nil {
		return true, err
	}
	if command == "provision-development-certificates" {
		registration := sourceRegistration{SourceOIN: options.sourceOIN, Name: options.sourceName}
		if registration.Name == "" {
			registration.Name = "FSC source " + registration.SourceOIN
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
	registration, err := loadSourceRegistration(options.sourcePath)
	if err != nil {
		return true, err
	}
	if registration.MetadataEndpoint.Transport == sourceTransportFSC || registration.DataAccess.Transport == sourceTransportFSC {
		return true, fmt.Errorf("FSC sources are discovered from contracts; use reconcile-fsc-sources")
	}
	validated, err := validateSourceOnline(ctx, dependencies.client, registration, options.outwayURL, options.schemaPath, options.publicBaseURL, dependencies.now())
	if err != nil {
		return true, err
	}
	if command == "validate-source" {
		_, _ = fmt.Fprintf(dependencies.stdout, "source %s is valid: metadata version %s, %d attestation type(s)\n", registration.SourceOIN, validated.Document.Version, len(validated.Publications))
		return true, nil
	}
	if options.dryRun {
		_, _ = fmt.Fprintf(dependencies.stdout, "dry-run valid for source %s: no keys, certificates, types or activation were written\n", registration.SourceOIN)
		return true, nil
	}
	store, err := dependencies.resolveCertificateStore(options)
	if err != nil {
		return true, err
	}
	backend, err := dependencies.resolveActivationBackend(options)
	if err != nil {
		return true, err
	}
	activation, err := activateSource(validated, store, backend)
	if err != nil {
		return true, err
	}
	_, _ = fmt.Fprintf(dependencies.stdout, "source %s activated: metadata version %s, %d type(s); storage=%s, certificate-store=%s\n", registration.SourceOIN, activation.MetadataVersion, len(activation.Types), options.storageBackend, options.certificateStoreName)
	return true, nil
}

func parseOnboardingOptions(command string, arguments []string, errorOutput io.Writer) (onboardingOptions, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(errorOutput)
	options := onboardingOptions{}
	if command == "provision-development-certificates" {
		set.StringVar(&options.sourceOIN, "source-oin", "", "20-digit OIN to bind the local development certificates to")
		set.StringVar(&options.sourceName, "source-name", "", "source name for the certificate subject (defaults to the FSC-derived name)")
	} else {
		set.StringVar(&options.sourcePath, "source", "", "path to a static HTTPS-mTLS source registration")
	}
	set.StringVar(&options.outwayURL, "outway-url", getEnv("FSC_OUTWAY_URL", "http://localhost:8087"), "FSC Outway base URL")
	set.StringVar(&options.schemaPath, "schema", "schemas/gbo-source-metadata-v1.schema.json", "source metadata JSON Schema")
	set.StringVar(&options.publicBaseURL, "type-metadata-base-url", getEnv("TYPE_METADATA_PUBLIC_BASE_URL", "http://localhost:9409"), "public Type Metadata base URL")
	set.StringVar(&options.readerPublicURL, "reader-public-url", os.Getenv("EUDI_PUBLIC_URL"), "public issuance-server URL whose host becomes the reader certificate DNS SAN")
	set.StringVar(&options.stateDir, "state-dir", ".local/onboarding", "filesystem onboarding state directory")
	set.StringVar(&options.secretsDir, "secrets-dir", ".local/secrets", "filesystem secret directory")
	if command == "onboard-source" {
		set.StringVar(&options.storageBackend, "storage-backend", getEnv("ONBOARDING_STORAGE_BACKEND", "filesystem"), "onboarding state backend")
		set.StringVar(&options.certificateStoreName, "certificate-store", getEnv("ONBOARDING_CERTIFICATE_STORE", "filesystem"), "store containing manually provisioned certificates")
		set.BoolVar(&options.dryRun, "dry-run", false, "validate without writing state")
	}
	if err := set.Parse(arguments); err != nil {
		return onboardingOptions{}, err
	}
	if set.NArg() != 0 {
		return onboardingOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	if command == "provision-development-certificates" {
		if !sourceOINPattern.MatchString(options.sourceOIN) {
			return onboardingOptions{}, fmt.Errorf("--source-oin must contain exactly 20 digits")
		}
	} else if options.sourcePath == "" {
		return onboardingOptions{}, fmt.Errorf("--source is required")
	}
	if strings.TrimSpace(options.outwayURL) == "" {
		return onboardingOptions{}, fmt.Errorf("--outway-url is required")
	}
	return options, nil
}

func activateSource(validated *validatedSourceRegistration, store certificateStore, backend activationBackend) (*sourceActivation, error) {
	if store == nil {
		return nil, fmt.Errorf("certificate store is required")
	}
	if backend == nil {
		return nil, fmt.Errorf("activation backend is required")
	}
	certificates, err := store.Load(validated.Registration)
	if err != nil {
		return nil, fmt.Errorf("load manually provisioned source-bound certificates: %w", err)
	}
	return backend.Activate(validated, certificates)
}

type filesystemActivationBackend struct {
	stateDir string
}

func newFilesystemActivationBackend(stateDir string) *filesystemActivationBackend {
	return &filesystemActivationBackend{stateDir: stateDir}
}

func (b *filesystemActivationBackend) Activate(validated *validatedSourceRegistration, certificates certificateArtifacts) (*sourceActivation, error) {
	typeStore := filepath.Join(b.stateDir, "type-metadata")
	activeStore := filepath.Join(b.stateDir, "active")
	for _, directory := range []string{typeStore, activeStore} {
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
		for _, definition := range validated.Document.eudiAttestations() {
			if definition.TypeID == publication.TypeID {
				offers = append([]sourceOffer(nil), definition.Offers...)
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
		})
	}
	payloadDigest := sha256.Sum256(validated.Payload)
	activation := &sourceActivation{
		SchemaVersion:              "1.0",
		Source:                     validated.Registration,
		MetadataURL:                validated.MetadataURL,
		MetadataVersion:            validated.Document.Version,
		MetadataPayloadDigest:      hex.EncodeToString(payloadDigest[:]),
		TypeMetadataStoreReference: typeStore,
		Types:                      types,
		Certificates:               certificates,
	}
	activationBytes, err := json.MarshalIndent(activation, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal active source registration: %w", err)
	}
	activationBytes = append(activationBytes, '\n')
	activationPath := filepath.Join(activeStore, validated.Registration.SourceOIN+".json")
	if err := writeSourceActivation(activationPath, activationBytes, activation); err != nil {
		return nil, fmt.Errorf("activate source registration: %w", err)
	}
	return activation, nil
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
	if existing.Source.SourceOIN != next.Source.SourceOIN {
		return fmt.Errorf("existing activation belongs to source %q", existing.Source.SourceOIN)
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
