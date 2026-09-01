package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gbo-demo/eudi-adapter/internal/onboarding"
	"gbo-demo/eudi-adapter/internal/postgresregistry"
)

type issuanceReleaseOptions struct {
	databaseURL    string
	databaseSchema string
	secretsDir     string
	templatePath   string
	adapterBaseURL string
	outputPath     string
}

func runIssuanceReleaseCommand(ctx context.Context, arguments []string, stdout, stderr io.Writer) (bool, error) {
	if len(arguments) == 0 || arguments[0] != "materialize-issuance-release" {
		return false, nil
	}
	set := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
	set.SetOutput(stderr)
	options := issuanceReleaseOptions{}
	set.StringVar(&options.databaseURL, "database-url", os.Getenv("SOURCE_REGISTRY_DATABASE_URL"), "read-only PostgreSQL Source Registry connection URL")
	set.StringVar(&options.databaseSchema, "database-schema", getEnv("SOURCE_REGISTRY_SCHEMA", "source_registry"), "PostgreSQL Source Registry schema")
	set.StringVar(&options.secretsDir, "secrets-dir", getEnv("ONBOARDING_SECRETS_DIR", "/source-certificates"), "mounted source certificate Secret directory")
	set.StringVar(&options.templatePath, "template", "/app/issuance_server.toml.example", "issuance-server TOML template")
	set.StringVar(&options.adapterBaseURL, "adapter-base-url", os.Getenv("EUDI_BRI_URL"), "public GBO adapter base URL")
	set.StringVar(&options.outputPath, "output", "/runtime/issuance_server.toml", "pod-local issuance-server TOML output")
	if err := set.Parse(arguments[1:]); err != nil {
		return true, err
	}
	if set.NArg() != 0 {
		return true, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	store, err := postgresregistry.Open(ctx, postgresregistry.Options{DatabaseURL: options.databaseURL, Schema: options.databaseSchema})
	if err != nil {
		return true, err
	}
	defer store.Close()
	release, err := store.ActiveRelease(ctx)
	if err != nil {
		return true, err
	}
	if err := materializeIssuanceRelease(options, release); err != nil {
		return true, err
	}
	_, _ = fmt.Fprintf(stdout, "issuance configuration materialized from Source Release %s\n", release.ID)
	return true, nil
}

func materializeIssuanceRelease(options issuanceReleaseOptions, release onboarding.SourceRelease) error {
	adapterBaseURL, err := parseAdapterBaseURL(options.adapterBaseURL)
	if err != nil {
		return err
	}
	templateBody, err := os.ReadFile(options.templatePath)
	if err != nil {
		return fmt.Errorf("read issuance config template: %w", err)
	}
	adapterTrustAnchors, templateBody, err := extractAdapterTrustAnchors(templateBody)
	if err != nil {
		return err
	}
	if release.ID == "" || len(release.Sources) == 0 {
		return fmt.Errorf("active Source Release is incomplete")
	}

	var settings strings.Builder
	metadataFiles := make([]string, 0)
	issuerAnchors := make(map[string]struct{})
	readerAnchors := make(map[string]struct{})
	seenOfferKeys := make(map[string]struct{})
	seenVCTs := make(map[string]struct{})

	for _, source := range release.Sources {
		candidate := onboarding.SourceCandidate{
			SourceID: source.SourceID, MetadataVersion: source.MetadataVersion,
			MetadataPayloadDigest: source.MetadataPayloadDigest, MetadataETag: source.MetadataETag,
			DeploymentDigest: source.DeploymentDigest, CheckedAt: source.CheckedAt,
			ExpiresAt: source.ExpiresAt, FreshUntil: source.FreshUntil, StaleUntil: source.StaleUntil,
			TransportAuthenticated: source.TransportAuthenticated, Snapshot: source.Snapshot,
			CertificateSet: source.CertificateSet, TypeMetadata: source.TypeMetadata,
		}
		activation, err := activationFromRegistryCandidate(candidate)
		if err != nil {
			return fmt.Errorf("source %s activation: %w", source.SourceID, err)
		}
		artifacts := certificateArtifactsForMountedSecret(options.secretsDir, source.CertificateSet.ID)
		mountedPublic, err := publicCertificateSet(source.CertificateSet.ID, artifacts)
		if err != nil {
			return fmt.Errorf("source %s public certificates: %w", source.SourceID, err)
		}
		if !reflect.DeepEqual(mountedPublic, source.CertificateSet) {
			return fmt.Errorf("source %s mounted certificate material does not match the active release", source.SourceID)
		}
		material, err := loadIssuanceCertificateMaterial(artifacts)
		if err != nil {
			return fmt.Errorf("source %s certificates: %w", source.SourceID, err)
		}
		issuerAnchors[material.issuerCA] = struct{}{}
		readerAnchors[material.readerCA] = struct{}{}
		metadataByVCT := make(map[string]onboarding.TypeMetadata, len(source.TypeMetadata))
		for _, metadata := range source.TypeMetadata {
			metadataByVCT[metadata.VCT] = metadata
		}
		for _, activatedType := range activation.Types {
			if _, duplicate := seenVCTs[activatedType.VCT]; duplicate {
				return fmt.Errorf("VCT %q is activated more than once", activatedType.VCT)
			}
			seenVCTs[activatedType.VCT] = struct{}{}
			metadata, found := metadataByVCT[activatedType.VCT]
			if !found {
				return fmt.Errorf("active Source Release has no Type Metadata for %s/%s", source.SourceID, activatedType.TypeID)
			}
			metadataDigest := sha256.Sum256(metadata.Bytes)
			metadataIntegrity := "sha256-" + base64.StdEncoding.EncodeToString(metadataDigest[:])
			if metadataIntegrity != activatedType.VCTIntegrity || metadataIntegrity != metadata.Integrity {
				return fmt.Errorf("active Type Metadata integrity mismatch for %s/%s", source.SourceID, activatedType.TypeID)
			}
			metadataName := fmt.Sprintf("type-%s-%s-v%s.json", source.SourceID, activatedType.TypeID, activatedType.TypeVersion)
			if err := os.MkdirAll(filepath.Dir(options.outputPath), 0o700); err != nil {
				return fmt.Errorf("create issuance runtime directory: %w", err)
			}
			if err := writeFileAtomically(filepath.Dir(options.outputPath), metadataName, metadata.Bytes, 0o644); err != nil {
				return fmt.Errorf("install Type Metadata %s: %w", metadataName, err)
			}
			metadataFiles = append(metadataFiles, "/config/"+metadataName)

			for _, offer := range activatedType.Offers {
				if _, duplicate := seenOfferKeys[offer.ID]; duplicate {
					return fmt.Errorf("issuance offer id %q is not globally unique", offer.ID)
				}
				seenOfferKeys[offer.ID] = struct{}{}
				endpoint := *adapterBaseURL
				endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/attestations/" + source.SourceID + "/" + activatedType.TypeID
				query := make(url.Values, len(offer.Parameters))
				for name, value := range offer.Parameters {
					formatted, err := canonicalOfferParameter(value)
					if err != nil {
						return fmt.Errorf("offer %q parameter %q: %w", offer.ID, name, err)
					}
					query.Set(name, formatted)
				}
				endpoint.RawQuery = query.Encode()
				appendDisclosureSettings(&settings, offer.ID, endpoint.String(), adapterTrustAnchors, material)
			}
			appendAttestationSettings(&settings, activatedType.VCT, material)
		}
	}
	if len(seenOfferKeys) == 0 {
		return fmt.Errorf("active Source Release contains no issuance offers")
	}
	sort.Strings(metadataFiles)
	rendered := string(templateBody)
	rendered = strings.Replace(rendered, "${EUDI_ONBOARDING_ISSUER_TRUST_ANCHOR}", renderTrustAnchors(issuerAnchors), 1)
	rendered = strings.Replace(rendered, "${EUDI_ONBOARDING_READER_TRUST_ANCHOR}", renderTrustAnchors(readerAnchors), 1)
	rendered, err = expandRequiredEnvironment(rendered)
	if err != nil {
		return err
	}
	rendered = strings.Replace(rendered, "{{TYPE_METADATA_FILES}}", renderStringList(metadataFiles, "  "), 1)
	rendered = strings.Replace(rendered, "{{GENERATED_ISSUANCE_SETTINGS}}", settings.String(), 1)
	if strings.Contains(rendered, "${") || strings.Contains(rendered, "{{") {
		return fmt.Errorf("issuance config template contains an unresolved placeholder")
	}
	if err := writeFileAtomically(filepath.Dir(options.outputPath), filepath.Base(options.outputPath), []byte(rendered), 0o600); err != nil {
		return fmt.Errorf("write issuance config: %w", err)
	}
	marker, err := json.MarshalIndent(struct {
		ReleaseID             string `json:"release_id"`
		ReleaseDigest         string `json:"release_digest"`
		MaterializationDigest string `json:"materialization_digest"`
	}{release.ID, release.Digest, release.MaterializationDigest}, "", "  ")
	if err != nil {
		return err
	}
	marker = append(marker, '\n')
	if err := writeFileAtomically(filepath.Dir(options.outputPath), "source-release.json", marker, 0o644); err != nil {
		return fmt.Errorf("write Source Release marker: %w", err)
	}
	return nil
}

func certificateArtifactsForMountedSecret(root, certificateSet string) certificateArtifacts {
	source := filepath.Join(root, certificateSet)
	ca := filepath.Join(root, "development-ca")
	return certificateArtifacts{
		IssuerKeyReference:    filepath.Join(source, "issuer-key.der.b64"),
		IssuerCertReference:   filepath.Join(source, "issuer-cert.der.b64"),
		ReaderKeyReference:    filepath.Join(source, "reader-key.der.b64"),
		ReaderCertReference:   filepath.Join(source, "reader-cert.der.b64"),
		StatusKeyReference:    filepath.Join(source, "status-key.der.b64"),
		StatusCertReference:   filepath.Join(source, "status-cert.der.b64"),
		IssuerCACertReference: filepath.Join(ca, "issuer-ca-cert.pem"),
		ReaderCACertReference: filepath.Join(ca, "reader-ca-cert.pem"),
	}
}
