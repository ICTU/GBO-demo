package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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
	SchemaVersion         string               `json:"schema_version"`
	Source                sourceRegistration   `json:"source"`
	MetadataURL           string               `json:"metadata_url"`
	MetadataVersion       string               `json:"metadata_version"`
	MetadataPayloadDigest string               `json:"metadata_payload_digest"`
	PublicJWKPath         string               `json:"public_jwk_path"`
	TypeMetadataStorePath string               `json:"type_metadata_store_path"`
	IssuanceEnvPath       string               `json:"issuance_env_path"`
	Types                 []activatedType      `json:"types"`
	Certificates          certificateArtifacts `json:"certificates"`
}

type activatedType struct {
	TypeID       string `json:"type_id"`
	TypeVersion  string `json:"type_version"`
	VCT          string `json:"vct"`
	VCTIntegrity string `json:"vct_integrity"`
}

type onboardingOptions struct {
	sourcePath    string
	environment   string
	dryRun        bool
	outwayURL     string
	schemaPath    string
	publicBaseURL string
	readerOrigin  string
	stateDir      string
	secretsDir    string
}

type onboardingDependencies struct {
	client              *http.Client
	now                 func() time.Time
	certificateProvider func(string, string) certificateProvider
	stdout              io.Writer
	stderr              io.Writer
}

func defaultOnboardingDependencies() onboardingDependencies {
	return onboardingDependencies{
		client: &http.Client{Timeout: 15 * time.Second},
		now:    time.Now,
		certificateProvider: func(root, readerOrigin string) certificateProvider {
			return newLocalCertificateProvider(root, readerOrigin)
		},
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

func runOnboardingCommand(ctx context.Context, arguments []string, dependencies onboardingDependencies) (bool, error) {
	if len(arguments) == 0 || (arguments[0] != "validate-source" && arguments[0] != "onboard-source") {
		return false, nil
	}
	command := arguments[0]
	options, err := parseOnboardingOptions(command, arguments[1:], dependencies.stderr)
	if err != nil {
		return true, err
	}
	registration, err := loadSourceRegistration(options.sourcePath)
	if err != nil {
		return true, err
	}
	validated, err := validateSourceOnline(ctx, dependencies.client, registration, options.outwayURL, options.schemaPath, options.publicBaseURL, dependencies.now())
	if err != nil {
		return true, err
	}
	if command == "validate-source" {
		_, _ = fmt.Fprintf(dependencies.stdout, "source %s is valid: metadata version %s, %d attestation type(s)\n", registration.SourceOIN, validated.Document.Version, len(validated.Publications))
		return true, nil
	}
	if options.environment != "local" {
		return true, fmt.Errorf("certificate provider for environment %q is not implemented; phase 4 supports only local", options.environment)
	}
	if options.dryRun {
		_, _ = fmt.Fprintf(dependencies.stdout, "dry-run valid for source %s: no keys, certificates, types or activation were written\n", registration.SourceOIN)
		return true, nil
	}
	provider := dependencies.certificateProvider(options.secretsDir, options.readerOrigin)
	activation, err := activateLocalSource(validated, options, provider)
	if err != nil {
		return true, err
	}
	_, _ = fmt.Fprintf(dependencies.stdout, "source %s activated locally: metadata version %s, %d type(s); private keys were written only below %s\n", registration.SourceOIN, activation.MetadataVersion, len(activation.Types), options.secretsDir)
	return true, nil
}

func parseOnboardingOptions(command string, arguments []string, errorOutput io.Writer) (onboardingOptions, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(errorOutput)
	options := onboardingOptions{}
	set.StringVar(&options.sourcePath, "source", "", "path to sources/<oin>.yaml")
	set.StringVar(&options.outwayURL, "outway-url", getEnv("FSC_OUTWAY_URL", "http://localhost:8087"), "FSC Outway base URL")
	set.StringVar(&options.schemaPath, "schema", "schemas/gbo-attestations-v1.schema.json", "source metadata JSON Schema")
	set.StringVar(&options.publicBaseURL, "type-metadata-base-url", getEnv("TYPE_METADATA_PUBLIC_BASE_URL", "http://localhost:9409"), "public Type Metadata base URL")
	set.StringVar(&options.readerOrigin, "reader-origin-url", os.Getenv("EUDI_READER_ORIGIN_URL"), "public reader origin embedded in the local reader certificate")
	set.StringVar(&options.stateDir, "state-dir", ".local/onboarding", "local onboarding state directory")
	set.StringVar(&options.secretsDir, "secrets-dir", ".local/secrets", "local secret backend directory")
	if command == "onboard-source" {
		set.StringVar(&options.environment, "env", "local", "certificate provider environment")
		set.BoolVar(&options.dryRun, "dry-run", false, "validate without writing state")
	}
	if err := set.Parse(arguments); err != nil {
		return onboardingOptions{}, err
	}
	if set.NArg() != 0 {
		return onboardingOptions{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	if options.sourcePath == "" {
		return onboardingOptions{}, fmt.Errorf("--source is required")
	}
	if strings.TrimSpace(options.outwayURL) == "" {
		return onboardingOptions{}, fmt.Errorf("--outway-url is required")
	}
	return options, nil
}

func activateLocalSource(validated *validatedSourceRegistration, options onboardingOptions, provider certificateProvider) (*sourceActivation, error) {
	if provider == nil {
		return nil, fmt.Errorf("local certificate provider is required")
	}
	certificates, err := provider.Provision(validated.Registration)
	if err != nil {
		return nil, fmt.Errorf("provision source-bound certificates: %w", err)
	}
	issuanceEnvPath, err := writeLocalIssuanceEnvironment(options.secretsDir, validated.Registration.SourceOIN, certificates)
	if err != nil {
		return nil, fmt.Errorf("prepare local issuance environment: %w", err)
	}
	typeStore := filepath.Join(options.stateDir, "type-metadata")
	trustStore := filepath.Join(options.stateDir, "trust")
	activeStore := filepath.Join(options.stateDir, "active")
	for _, directory := range []string{typeStore, trustStore, activeStore} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("create onboarding state directory %q: %w", directory, err)
		}
	}
	for _, publication := range validated.Publications {
		if err := persistTypeMetadataPublication(typeStore, publication); err != nil {
			return nil, err
		}
	}
	thumbprint := strings.TrimPrefix(validated.Registration.MetadataSigningJWKThumbprint, "sha256-")
	publicJWKPath := filepath.Join(trustStore, validated.Registration.SourceOIN+"-"+thumbprint+".jwk")
	if err := writeSameOrCreate(publicJWKPath, validated.PublicJWK, 0o644); err != nil {
		return nil, fmt.Errorf("persist pinned source metadata JWK: %w", err)
	}
	types := make([]activatedType, 0, len(validated.Publications))
	for index, publication := range validated.Publications {
		definition := validated.Document.Attestations[index]
		types = append(types, activatedType{
			TypeID:       definition.TypeID,
			TypeVersion:  definition.TypeVersion,
			VCT:          publication.VCT,
			VCTIntegrity: publication.Integrity,
		})
	}
	payloadDigest := sha256.Sum256(validated.Payload)
	activation := &sourceActivation{
		SchemaVersion:         "1.0",
		Source:                validated.Registration,
		MetadataURL:           validated.MetadataURL,
		MetadataVersion:       validated.Document.Version,
		MetadataPayloadDigest: hex.EncodeToString(payloadDigest[:]),
		PublicJWKPath:         publicJWKPath,
		TypeMetadataStorePath: typeStore,
		IssuanceEnvPath:       issuanceEnvPath,
		Types:                 types,
		Certificates:          certificates,
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

func writeLocalIssuanceEnvironment(secretRoot, sourceOIN string, certificates certificateArtifacts) (string, error) {
	readBase64 := func(path string) (string, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(string(raw))
		if value == "" {
			return "", fmt.Errorf("file %q is empty", path)
		}
		return value, nil
	}
	values := make(map[string]string)
	for name, path := range map[string]string{
		"EUDI_ISSUER_KEY":  certificates.IssuerKeyPath,
		"EUDI_ISSUER_CERT": certificates.IssuerCertPath,
		"EUDI_READER_KEY":  certificates.ReaderKeyPath,
		"EUDI_READER_CERT": certificates.ReaderCertPath,
		"EUDI_STATUS_KEY":  certificates.StatusKeyPath,
		"EUDI_STATUS_CERT": certificates.StatusCertPath,
	} {
		value, err := readBase64(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		values[name] = value
	}
	for name, path := range map[string]string{
		"EUDI_ONBOARDING_ISSUER_TRUST_ANCHOR": certificates.IssuerCACertPath,
		"EUDI_ONBOARDING_READER_TRUST_ANCHOR": certificates.ReaderCACertPath,
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		block, _ := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return "", fmt.Errorf("%s file %q is not a PEM certificate", name, path)
		}
		values[name] = `"` + base64.StdEncoding.EncodeToString(block.Bytes) + `",`
	}
	order := []string{
		"EUDI_ISSUER_KEY", "EUDI_ISSUER_CERT", "EUDI_READER_KEY", "EUDI_READER_CERT",
		"EUDI_STATUS_KEY", "EUDI_STATUS_CERT", "EUDI_ONBOARDING_ISSUER_TRUST_ANCHOR",
		"EUDI_ONBOARDING_READER_TRUST_ANCHOR",
	}
	var body strings.Builder
	body.WriteString("# Generated by onboard-source --env local; contains private keys.\n")
	for _, name := range order {
		_, _ = fmt.Fprintf(&body, "%s='%s'\n", name, values[name])
	}
	path := filepath.Join(secretRoot, sourceOIN, "issuance.env")
	if err := writeSameOrCreate(path, []byte(body.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
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
	if comparison == 0 {
		return fmt.Errorf("metadata version %q has different activation bytes", next.MetadataVersion)
	}
	return writeFileAtomically(filepath.Dir(path), filepath.Base(path), body, 0o644)
}

func writeSameOrCreate(path string, body []byte, mode os.FileMode) error {
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, body) {
			return fmt.Errorf("existing file %q contains different bytes", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeFileAtomically(filepath.Dir(path), filepath.Base(path), body, mode)
}
