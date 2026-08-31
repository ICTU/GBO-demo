package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

const adapterTrustAnchorMarker = "# GBO_GENERATOR_ADAPTER_"

type publicIssuanceOffer struct {
	Key             string         `json:"key"`
	Label           string         `json:"label"`
	Description     string         `json:"description,omitempty"`
	AttestationType string         `json:"attestation_type"`
	SourceID        string         `json:"source_id"`
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

func expandRequiredEnvironment(template string) (string, error) {
	missing := make(map[string]struct{})
	expanded := os.Expand(template, func(name string) string {
		value, exists := os.LookupEnv(name)
		if !exists || value == "" {
			missing[name] = struct{}{}
		}
		return value
	})
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		return "", fmt.Errorf("issuance config template requires unset environment variable(s): %s", strings.Join(names, ", "))
	}
	return expanded, nil
}

func parseAdapterBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("adapter base URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	return parsed, nil
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
	statusPath := "/tsl/" + hex.EncodeToString(statusDigest[:])
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
