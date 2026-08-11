package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

var (
	sourceOINPattern        = regexp.MustCompile(`^[0-9]{20}$`)
	serviceReferencePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

const (
	sourceTransportFSC       = "fsc"
	sourceTransportHTTPSMTLS = "https-mtls"
)

type sourceMetadataEndpoint struct {
	Transport        string `json:"transport" yaml:"transport"`
	ServiceReference string `json:"service_reference,omitempty" yaml:"service_reference,omitempty"`
	Path             string `json:"path,omitempty" yaml:"path,omitempty"`
	Endpoint         string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
}

type sourceDataAccess struct {
	Transport        string `json:"transport" yaml:"transport"`
	ServiceReference string `json:"service_reference,omitempty" yaml:"service_reference,omitempty"`
}

type sourceRegistration struct {
	SourceOIN        string                 `json:"source_oin" yaml:"source_oin"`
	Name             string                 `json:"name" yaml:"name"`
	MetadataEndpoint sourceMetadataEndpoint `json:"metadata_endpoint" yaml:"metadata_endpoint"`
	DataAccess       sourceDataAccess       `json:"data_access" yaml:"data_access"`
}

type validatedSourceRegistration struct {
	Registration sourceRegistration
	Document     sourceMetadataDocument
	Payload      []byte
	MetadataURL  string
	Publications []*typeMetadataPublication
}

func loadSourceRegistration(path string) (sourceRegistration, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return sourceRegistration{}, fmt.Errorf("read source registration: %w", err)
	}
	registration, err := parseSourceRegistrationYAML(raw)
	if err != nil {
		return sourceRegistration{}, err
	}
	if err := registration.validate(); err != nil {
		return sourceRegistration{}, err
	}
	if err := validateSourceRegistrationUniqueness(path, registration); err != nil {
		return sourceRegistration{}, err
	}
	return registration, nil
}

func parseSourceRegistrationYAML(raw []byte) (sourceRegistration, error) {
	var registration sourceRegistration
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&registration); err != nil {
		return sourceRegistration{}, fmt.Errorf("parse source registration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return sourceRegistration{}, fmt.Errorf("parse source registration: multiple YAML documents are not supported")
	}
	return registration, nil
}

func (r sourceRegistration) validate() error {
	if !sourceOINPattern.MatchString(r.SourceOIN) {
		return fmt.Errorf("source registration source_oin must contain exactly 20 digits")
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("source registration name is required")
	}
	if err := r.MetadataEndpoint.validate(); err != nil {
		return fmt.Errorf("source registration metadata_endpoint: %w", err)
	}
	if err := r.DataAccess.validate(); err != nil {
		return fmt.Errorf("source registration data_access: %w", err)
	}
	if r.MetadataEndpoint.Transport == sourceTransportFSC && r.DataAccess.Transport == sourceTransportFSC && r.MetadataEndpoint.ServiceReference == r.DataAccess.ServiceReference {
		return fmt.Errorf("source registration metadata and data FSC services must be separate")
	}
	return nil
}

func (e sourceMetadataEndpoint) validate() error {
	switch e.Transport {
	case sourceTransportFSC:
		if !serviceReferencePattern.MatchString(e.ServiceReference) {
			return fmt.Errorf("service_reference is invalid")
		}
		if err := validateAbsoluteURLPath(e.Path); err != nil {
			return fmt.Errorf("path: %w", err)
		}
		if e.Endpoint != "" {
			return fmt.Errorf("endpoint is not allowed for FSC transport")
		}
	case sourceTransportHTTPSMTLS:
		if e.ServiceReference != "" || e.Path != "" {
			return fmt.Errorf("service_reference and path are not allowed for https-mtls transport")
		}
		if err := validateAbsoluteHTTPSEndpoint(e.Endpoint); err != nil {
			return fmt.Errorf("endpoint: %w", err)
		}
	default:
		return fmt.Errorf("unsupported transport %q", e.Transport)
	}
	return nil
}

func (a sourceDataAccess) validate() error {
	switch a.Transport {
	case sourceTransportFSC:
		if !serviceReferencePattern.MatchString(a.ServiceReference) {
			return fmt.Errorf("service_reference is invalid")
		}
	case sourceTransportHTTPSMTLS:
		if a.ServiceReference != "" {
			return fmt.Errorf("service_reference is not allowed for https-mtls transport")
		}
	default:
		return fmt.Errorf("unsupported transport %q", a.Transport)
	}
	return nil
}

func validateAbsoluteURLPath(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || parsed.IsAbs() || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an absolute URL path without query or fragment")
	}
	return nil
}

func validateAbsoluteHTTPSEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an absolute HTTPS URL without credentials, query or fragment")
	}
	return nil
}

func (r sourceRegistration) metadataURL(outwayURL string) (string, error) {
	switch r.MetadataEndpoint.Transport {
	case sourceTransportFSC:
		if strings.TrimSpace(outwayURL) == "" {
			return "", fmt.Errorf("FSC Outway URL is required")
		}
		return strings.TrimRight(outwayURL, "/") + "/" + r.MetadataEndpoint.ServiceReference + r.MetadataEndpoint.Path, nil
	case sourceTransportHTTPSMTLS:
		return "", fmt.Errorf("source metadata transport %q is configured but not implemented", sourceTransportHTTPSMTLS)
	default:
		return "", fmt.Errorf("unsupported source metadata transport %q", r.MetadataEndpoint.Transport)
	}
}

func validateSourceRegistrationUniqueness(path string, registration sourceRegistration) error {
	entries, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.yaml"))
	if err != nil {
		return fmt.Errorf("list source registrations: %w", err)
	}
	current, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve source registration path: %w", err)
	}
	for _, candidate := range entries {
		absolute, err := filepath.Abs(candidate)
		if err != nil || absolute == current {
			continue
		}
		raw, err := os.ReadFile(candidate)
		if err != nil {
			return fmt.Errorf("read source registration %q: %w", candidate, err)
		}
		other, err := parseSourceRegistrationYAML(raw)
		if err != nil {
			return fmt.Errorf("parse source registration %q: %w", candidate, err)
		}
		if other.SourceOIN == registration.SourceOIN {
			return fmt.Errorf("source OIN %q is registered more than once", registration.SourceOIN)
		}
	}
	return nil
}

func validateSourceOnline(ctx context.Context, client *http.Client, registration sourceRegistration, outwayURL, schemaPath, publicBaseURL string, now time.Time) (*validatedSourceRegistration, error) {
	if client == nil {
		return nil, fmt.Errorf("source validation HTTP client is required")
	}
	if err := validateTypeMetadataBaseURL(publicBaseURL); err != nil {
		return nil, err
	}
	metadataURL, err := registration.metadataURL(outwayURL)
	if err != nil {
		return nil, err
	}
	payload, err := fetchSourceMetadata(ctx, client, metadataURL, registration.MetadataEndpoint.Transport)
	if err != nil {
		return nil, err
	}
	if err := validateSourceMetadataSchema(payload, schemaPath); err != nil {
		return nil, err
	}
	document, err := decodeSourceMetadataDocument(payload)
	if err != nil {
		return nil, err
	}
	if document.SourceOIN != registration.SourceOIN {
		return nil, fmt.Errorf("source metadata OIN %q does not match registered OIN %q", document.SourceOIN, registration.SourceOIN)
	}
	if document.SchemaVersion != "1.0" {
		return nil, fmt.Errorf("unsupported source metadata schema_version %q", document.SchemaVersion)
	}
	if len(document.Attestations) == 0 {
		return nil, fmt.Errorf("source metadata must contain at least one attestation")
	}
	if _, err := compareNumericVersion(document.Version, "0"); err != nil {
		return nil, fmt.Errorf("source metadata version: %w", err)
	}
	seenTypes := make(map[string]bool, len(document.Attestations))
	publications := make([]*typeMetadataPublication, 0, len(document.Attestations))
	for _, definition := range document.Attestations {
		if seenTypes[definition.TypeID] {
			return nil, fmt.Errorf("source metadata contains duplicate type_id %q", definition.TypeID)
		}
		seenTypes[definition.TypeID] = true
		if err := validateSourceAttestation(definition); err != nil {
			return nil, fmt.Errorf("attestation %q: %w", definition.TypeID, err)
		}
		if err := validateGraphQLEndpoint(definition.GraphQL.Endpoint, registration.DataAccess.Transport); err != nil {
			return nil, fmt.Errorf("attestation %q graphql.endpoint: %w", definition.TypeID, err)
		}
		if _, err := validateSourceMetadataEnvelope(document, definition, now, defaultSourceMetadataCachePolicy); err != nil {
			return nil, err
		}
		publication, err := newTypeMetadataPublication(publicBaseURL, document.SourceOIN, definition)
		if err != nil {
			return nil, fmt.Errorf("materialise type metadata for %q: %w", definition.TypeID, err)
		}
		publications = append(publications, publication)
	}
	return &validatedSourceRegistration{
		Registration: registration,
		Document:     document,
		Payload:      append([]byte(nil), payload...),
		MetadataURL:  metadataURL,
		Publications: publications,
	}, nil
}

func fetchSourceMetadata(ctx context.Context, client *http.Client, metadataURL, transport string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create source metadata validation request: %w", err)
	}
	req.Header.Set("Accept", sourceMetadataMediaType)
	if transport == sourceTransportFSC {
		txID, err := newFscTransactionID()
		if err != nil {
			return nil, fmt.Errorf("generate source metadata Fsc-Transaction-Id: %w", err)
		}
		req.Header.Set("Fsc-Transaction-Id", txID)
	} else {
		return nil, fmt.Errorf("source metadata transport %q is configured but not implemented", transport)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch source metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source metadata request returned status %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != sourceMetadataMediaType {
		return nil, fmt.Errorf("source metadata content type must be %s", sourceMetadataMediaType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read source metadata: %w", err)
	}
	return body, nil
}

func decodeSourceMetadataDocument(payload []byte) (sourceMetadataDocument, error) {
	var document sourceMetadataDocument
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return sourceMetadataDocument{}, fmt.Errorf("parse source metadata payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return sourceMetadataDocument{}, fmt.Errorf("parse source metadata payload: trailing JSON data")
	}
	return document, nil
}

func validateSourceMetadataSchema(payload []byte, schemaPath string) error {
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	mappingPath := filepath.Join(filepath.Dir(schemaPath), "gbo-simple-v1.schema.json")
	mappingFile, err := os.Open(mappingPath)
	if err != nil {
		return fmt.Errorf("open mapping profile schema: %w", err)
	}
	defer mappingFile.Close()
	mappingSchema, err := jsonschema.UnmarshalJSON(mappingFile)
	if err != nil {
		return fmt.Errorf("parse mapping profile schema: %w", err)
	}
	if err := compiler.AddResource("urn:gov:nl:gbo:schema:gbo-simple-v1:1", mappingSchema); err != nil {
		return fmt.Errorf("register mapping profile schema: %w", err)
	}
	schema, err := compiler.Compile(schemaPath)
	if err != nil {
		return fmt.Errorf("compile source metadata schema: %w", err)
	}
	metadata, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("parse source metadata for schema validation: %w", err)
	}
	if err := schema.Validate(metadata); err != nil {
		return fmt.Errorf("source metadata does not match gbo-attestations-v1: %w", err)
	}
	return nil
}
