package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	qtspCertificateSetID = "qtsp-reference"
	qtspUsecase          = "qtsp-attestation"
	qtspVCT              = "https://gbo.ictu.nl/credentials/qtsp-attestation/v1"
	qtspMetadataFilename = "type-qtsp-attestation-v1.json"
)

func qtspRegistration(oin, name string) sourceRegistration {
	paths := make([][]string, 0, 8)
	for _, claim := range []string{"family_name", "given_name", "birthdate", "nationalities"} {
		paths = append(paths, []string{"urn:eudi:pid:nl:1", claim}, []string{claim})
	}
	return sourceRegistration{
		SourceID: qtspCertificateSetID, SourceOIN: oin, Name: name,
		Description:      "Reference QTSP in the GBO test environment; not a qualified trust service",
		ReaderAttributes: map[string][][]string{"urn:eudi:pid:nl:1": paths},
	}
}

func runQTSPIssuanceProvisionCommand(arguments []string, stdout, stderr io.Writer) (bool, error) {
	if len(arguments) == 0 || arguments[0] != "provision-qtsp-issuance-certificates" {
		return false, nil
	}
	set := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
	set.SetOutput(stderr)
	oin := set.String("qtsp-oin", "99999999900000001300", "20-digit development OIN of the reference QTSP")
	name := set.String("qtsp-name", "GBO demo test QTSP", "reference QTSP certificate organisation")
	readerPublicURL := set.String("reader-public-url", os.Getenv("EUDI_PUBLIC_URL"), "public issuance-server URL")
	secretsDir := set.String("secrets-dir", ".local/secrets", "filesystem secret directory")
	tlsHost := set.String("tls-host", "qtsp-reference", "internal attestation callback DNS name")
	if err := set.Parse(arguments[1:]); err != nil {
		return true, err
	}
	if set.NArg() != 0 {
		return true, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	if !sourceOINPattern.MatchString(*oin) {
		return true, fmt.Errorf("--qtsp-oin must contain exactly 20 digits")
	}
	if strings.TrimSpace(*name) == "" {
		return true, fmt.Errorf("--qtsp-name is required")
	}
	if _, err := parseDevelopmentRootURL(*readerPublicURL, true); err != nil {
		return true, fmt.Errorf("--reader-public-url %w", err)
	}
	registration := qtspRegistration(*oin, *name)
	provider := newDevelopmentCAProvider(*secretsDir, *readerPublicURL)
	if _, err := provider.Provision(registration); err != nil {
		return true, fmt.Errorf("provision QTSP issuance certificates: %w", err)
	}
	if err := provider.ProvisionTLSServerCertificate(registration, *tlsHost); err != nil {
		return true, fmt.Errorf("provision QTSP callback TLS certificate: %w", err)
	}
	_, _ = fmt.Fprintln(stdout, "development issuance and callback certificates provisioned for the reference QTSP")
	return true, nil
}

func appendQTSPDisclosureSettings(builder *strings.Builder, callbackURL, callbackTrustAnchor string, material issuanceCertificateMaterial) {
	key := strconv.Quote(qtspUsecase)
	_, _ = fmt.Fprintf(builder, "\n[disclosure_settings.%s]\nprivate_key_type = \"software\"\nprivate_key = %s\ncertificate = %s\n\n", key, strconv.Quote(material.readerKey), strconv.Quote(material.readerCert))
	_, _ = fmt.Fprintf(builder, "[[disclosure_settings.%s.dcql_query.credentials]]\nid = \"pid_credential\"\nformat = \"dc+sd-jwt\"\nmeta = { vct_values = [\"urn:eudi:pid:nl:1\"] }\nclaims = [\n", key)
	for _, claim := range []string{"family_name", "given_name", "birthdate", "nationalities"} {
		_, _ = fmt.Fprintf(builder, "  { path = [%s] },\n", strconv.Quote(claim))
	}
	_, _ = fmt.Fprintf(builder, "]\n\n[disclosure_settings.%s.attestation_url_config]\nbase_url = %s\ntrust_anchors = [%s]\n", key, strconv.Quote(callbackURL), strconv.Quote(callbackTrustAnchor))
	appendAttestationSettingsWithValidity(builder, qtspVCT, material, 30, 1, "EAA")
}

func installQTSPMetadata(sourcePath, outputPath string) (string, error) {
	body, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", fmt.Errorf("read QTSP Type Metadata: %w", err)
	}
	var metadata struct {
		VCT string `json:"vct"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil || metadata.VCT != qtspVCT {
		return "", fmt.Errorf("QTSP Type Metadata must be JSON for VCT %s", qtspVCT)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomically(filepath.Dir(outputPath), qtspMetadataFilename, body, 0o644); err != nil {
		return "", err
	}
	return "/config/" + qtspMetadataFilename, nil
}

func validateQTSPCallbackURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("QTSP attestation URL must be an absolute HTTPS URL without credentials, query or fragment")
	}
	return nil
}
