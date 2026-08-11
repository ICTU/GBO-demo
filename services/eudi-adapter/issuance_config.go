package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const adapterTrustAnchorMarker = "# GBO_GENERATOR_ADAPTER_"

type issuanceConfigOptions struct {
	activationsDir string
	templatePath   string
	adapterBaseURL string
	outputPath     string
	offersPath     string
}

type publicIssuanceOffer struct {
	Key             string         `json:"key"`
	Label           string         `json:"label"`
	Description     string         `json:"description,omitempty"`
	AttestationType string         `json:"attestation_type"`
	SourceOIN       string         `json:"source_oin"`
	TypeID          string         `json:"type_id"`
	Parameters      map[string]any `json:"parameters"`
}

type issuanceCertificateMaterial struct {
	issuerKey  string
	issuerCert string
	readerKey  string
	readerCert string
	statusKey  string
	statusCert string
	issuerCA   string
	readerCA   string
}

func runIssuanceConfigCommand(arguments []string, stdout, stderr io.Writer) (bool, error) {
	if len(arguments) == 0 || arguments[0] != "generate-issuance-config" {
		return false, nil
	}
	set := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
	set.SetOutput(stderr)
	options := issuanceConfigOptions{}
	set.StringVar(&options.activationsDir, "activations-dir", ".local/onboarding/active", "directory containing active source registrations")
	set.StringVar(&options.templatePath, "template", "services/eudi-issuance-server/config/issuance_server.toml.example", "issuance-server TOML template")
	set.StringVar(&options.adapterBaseURL, "adapter-base-url", os.Getenv("EUDI_BRI_URL"), "public GBO adapter base URL")
	set.StringVar(&options.outputPath, "output", "services/eudi-issuance-server/config/issuance_server.toml", "generated issuance-server TOML")
	set.StringVar(&options.offersPath, "offers-output", "services/eudi-issuance-server/config/eudi-offers.json", "generated public issuance offers")
	if err := set.Parse(arguments[1:]); err != nil {
		return true, err
	}
	if set.NArg() != 0 {
		return true, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	if err := generateIssuanceConfig(options); err != nil {
		return true, err
	}
	_, _ = fmt.Fprintf(stdout, "issuance configuration generated from active source offers: %s; offers: %s\n", options.outputPath, options.offersPath)
	return true, nil
}

func generateIssuanceConfig(options issuanceConfigOptions) error {
	adapterBaseURL, err := parseAdapterBaseURL(options.adapterBaseURL)
	if err != nil {
		return err
	}
	activations, err := loadIssuanceActivations(options.activationsDir)
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

	var settings strings.Builder
	metadataFiles := make([]string, 0)
	offers := make([]publicIssuanceOffer, 0)
	issuerAnchors := make(map[string]struct{})
	readerAnchors := make(map[string]struct{})
	seenOfferKeys := make(map[string]struct{})
	seenVCTs := make(map[string]struct{})

	for _, activation := range activations {
		material, err := loadIssuanceCertificateMaterial(activation.Certificates)
		if err != nil {
			return fmt.Errorf("source %s certificates: %w", activation.Source.SourceOIN, err)
		}
		issuerAnchors[material.issuerCA] = struct{}{}
		readerAnchors[material.readerCA] = struct{}{}
		for _, activatedType := range activation.Types {
			if _, duplicate := seenVCTs[activatedType.VCT]; duplicate {
				return fmt.Errorf("VCT %q is activated more than once", activatedType.VCT)
			}
			seenVCTs[activatedType.VCT] = struct{}{}

			metadataName := fmt.Sprintf("type-%s-%s-v%s.json", activation.Source.SourceOIN, activatedType.TypeID, activatedType.TypeVersion)
			metadataBody, err := os.ReadFile(activatedType.TypeMetadataReference)
			if err != nil {
				return fmt.Errorf("read activated Type Metadata for %s/%s: %w", activation.Source.SourceOIN, activatedType.TypeID, err)
			}
			if err := os.MkdirAll(filepath.Dir(options.outputPath), 0o755); err != nil {
				return fmt.Errorf("create issuance config directory: %w", err)
			}
			if err := writeFileAtomically(filepath.Dir(options.outputPath), metadataName, metadataBody, 0o644); err != nil {
				return fmt.Errorf("install Type Metadata %s: %w", metadataName, err)
			}
			metadataFiles = append(metadataFiles, "/config/"+metadataName)

			for _, offer := range activatedType.Offers {
				if _, duplicate := seenOfferKeys[offer.ID]; duplicate {
					return fmt.Errorf("issuance offer id %q is not globally unique", offer.ID)
				}
				seenOfferKeys[offer.ID] = struct{}{}
				endpoint := *adapterBaseURL
				endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/attestations/" + activation.Source.SourceOIN + "/" + activatedType.TypeID
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
				offers = append(offers, publicIssuanceOffer{
					Key: offer.ID, Label: offer.Label, Description: offer.Description,
					AttestationType: activatedType.VCT, SourceOIN: activation.Source.SourceOIN,
					TypeID: activatedType.TypeID, Parameters: offer.Parameters,
				})
			}
			appendAttestationSettings(&settings, activatedType.VCT, material)
		}
	}
	if len(offers) == 0 {
		return fmt.Errorf("active source registrations contain no issuance offers")
	}
	sort.Strings(metadataFiles)
	sort.Slice(offers, func(i, j int) bool { return offers[i].Key < offers[j].Key })

	rendered := string(templateBody)
	rendered = strings.Replace(rendered, "${EUDI_ONBOARDING_ISSUER_TRUST_ANCHOR}", renderTrustAnchors(issuerAnchors), 1)
	rendered = strings.Replace(rendered, "${EUDI_ONBOARDING_READER_TRUST_ANCHOR}", renderTrustAnchors(readerAnchors), 1)
	rendered = strings.Replace(rendered, "{{TYPE_METADATA_FILES}}", renderStringList(metadataFiles, "  "), 1)
	rendered = strings.Replace(rendered, "{{GENERATED_ISSUANCE_SETTINGS}}", settings.String(), 1)
	rendered = os.ExpandEnv(rendered)
	if strings.Contains(rendered, "${") || strings.Contains(rendered, "{{") {
		return fmt.Errorf("issuance config template contains an unresolved placeholder")
	}
	if err := writeFileAtomically(filepath.Dir(options.outputPath), filepath.Base(options.outputPath), []byte(rendered), 0o600); err != nil {
		return fmt.Errorf("write issuance config: %w", err)
	}
	offersBody, err := json.MarshalIndent(offers, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal public issuance offers: %w", err)
	}
	offersBody = append(offersBody, '\n')
	if err := os.MkdirAll(filepath.Dir(options.offersPath), 0o755); err != nil {
		return fmt.Errorf("create offers output directory: %w", err)
	}
	if err := writeFileAtomically(filepath.Dir(options.offersPath), filepath.Base(options.offersPath), offersBody, 0o644); err != nil {
		return fmt.Errorf("write public issuance offers: %w", err)
	}
	return nil
}

func parseAdapterBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("adapter base URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	return parsed, nil
}

func loadIssuanceActivations(directory string) ([]sourceActivation, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read active source registrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	activations := make([]sourceActivation, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read activation %s: %w", entry.Name(), err)
		}
		var activation sourceActivation
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&activation); err != nil {
			return nil, fmt.Errorf("parse activation %s: %w", entry.Name(), err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			return nil, fmt.Errorf("parse activation %s: trailing JSON data", entry.Name())
		}
		if activation.SchemaVersion != "1.0" || !sourceOINPattern.MatchString(activation.Source.SourceOIN) || len(activation.Types) == 0 {
			return nil, fmt.Errorf("activation %s is incomplete", entry.Name())
		}
		for _, activatedType := range activation.Types {
			if activatedType.TypeID == "" || activatedType.VCT == "" || activatedType.TypeMetadataReference == "" || len(activatedType.Offers) == 0 {
				return nil, fmt.Errorf("activation %s contains an incomplete attestation type", entry.Name())
			}
		}
		activations = append(activations, activation)
	}
	if len(activations) == 0 {
		return nil, fmt.Errorf("active source directory contains no registrations")
	}
	return activations, nil
}

func extractAdapterTrustAnchors(templateBody []byte) (string, []byte, error) {
	lines := strings.Split(string(templateBody), "\n")
	found := ""
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, adapterTrustAnchorMarker) {
			if found != "" {
				return "", nil, fmt.Errorf("issuance config template contains multiple adapter trust anchor markers")
			}
			found = strings.TrimPrefix(line, adapterTrustAnchorMarker)
			continue
		}
		kept = append(kept, line)
	}
	if !strings.HasPrefix(found, "trust_anchors = [") {
		return "", nil, fmt.Errorf("issuance config template has no adapter trust anchor marker")
	}
	return found, []byte(strings.Join(kept, "\n")), nil
}

func loadIssuanceCertificateMaterial(artifacts certificateArtifacts) (issuanceCertificateMaterial, error) {
	read := func(path string) (string, error) {
		body, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		value := strings.TrimSpace(string(body))
		if value == "" {
			return "", fmt.Errorf("certificate material %q is empty", path)
		}
		return value, nil
	}
	anchor := func(path string) (string, error) {
		body, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		block, rest := pem.Decode(body)
		if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
			return "", fmt.Errorf("CA reference %q must contain exactly one PEM certificate", path)
		}
		return base64.StdEncoding.EncodeToString(block.Bytes), nil
	}
	var material issuanceCertificateMaterial
	var err error
	for target, path := range map[*string]string{
		&material.issuerKey: artifacts.IssuerKeyReference, &material.issuerCert: artifacts.IssuerCertReference,
		&material.readerKey: artifacts.ReaderKeyReference, &material.readerCert: artifacts.ReaderCertReference,
		&material.statusKey: artifacts.StatusKeyReference, &material.statusCert: artifacts.StatusCertReference,
	} {
		if *target, err = read(path); err != nil {
			return issuanceCertificateMaterial{}, err
		}
	}
	if material.issuerCA, err = anchor(artifacts.IssuerCACertReference); err != nil {
		return issuanceCertificateMaterial{}, err
	}
	if material.readerCA, err = anchor(artifacts.ReaderCACertReference); err != nil {
		return issuanceCertificateMaterial{}, err
	}
	return material, nil
}

func canonicalOfferParameter(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		if typed != float64(int64(typed)) {
			return "", fmt.Errorf("numeric offer values must be integers")
		}
		return strconv.FormatInt(int64(typed), 10), nil
	case int:
		return strconv.Itoa(typed), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case json.Number:
		integer, err := typed.Int64()
		if err != nil {
			return "", fmt.Errorf("numeric offer values must be integers")
		}
		return strconv.FormatInt(integer, 10), nil
	default:
		return "", fmt.Errorf("unsupported offer value type %T", value)
	}
}

func appendDisclosureSettings(builder *strings.Builder, key, endpoint, adapterTrustAnchors string, material issuanceCertificateMaterial) {
	quotedKey := strconv.Quote(key)
	_, _ = fmt.Fprintf(builder, "\n[disclosure_settings.%s]\nprivate_key_type = \"software\"\nprivate_key = %s\ncertificate = %s\n\n", quotedKey, strconv.Quote(material.readerKey), strconv.Quote(material.readerCert))
	_, _ = fmt.Fprintf(builder, "[[disclosure_settings.%s.dcql_query.credentials]]\nid = \"pid_credential\"\nformat = \"dc+sd-jwt\"\nmeta = { vct_values = [\"urn:eudi:pid:nl:1\"] }\nclaims = [ { path = [\"bsn\"] } ]\n\n", quotedKey)
	_, _ = fmt.Fprintf(builder, "[disclosure_settings.%s.attestation_url_config]\nbase_url = %s\n%s\n", quotedKey, strconv.Quote(endpoint), adapterTrustAnchors)
}

func appendAttestationSettings(builder *strings.Builder, vct string, material issuanceCertificateMaterial) {
	statusDigest := sha256.Sum256([]byte(vct))
	statusPath := "/tsl/" + hex.EncodeToString(statusDigest[:6])
	quotedVCT := strconv.Quote(vct)
	_, _ = fmt.Fprintf(builder, "\n[attestation_settings.%s]\nvalid_days = 365\ncopies_per_format = { \"dc+sd-jwt\" = 4 }\nprivate_key_type = \"software\"\nprivate_key = %s\ncertificate = %s\n\n", quotedVCT, strconv.Quote(material.issuerKey), strconv.Quote(material.issuerCert))
	_, _ = fmt.Fprintf(builder, "[attestation_settings.%s.status_list]\ncontext_path = %s\npublish_dir = \"/tsl-publish\"\nprivate_key_type = \"software\"\nprivate_key = %s\ncertificate = %s\n", quotedVCT, strconv.Quote(statusPath), strconv.Quote(material.statusKey), strconv.Quote(material.statusCert))
}

func renderTrustAnchors(anchors map[string]struct{}) string {
	values := make([]string, 0, len(anchors))
	for value := range anchors {
		values = append(values, value)
	}
	sort.Strings(values)
	return renderStringList(values, "  ")
}

func renderStringList(values []string, indent string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = indent + strconv.Quote(value) + ","
	}
	return strings.Join(quoted, "\n")
}
