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
)

var (
	peerIDPattern           = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)
	sourceOINPattern        = regexp.MustCompile(`^[0-9]{20}$`)
	sourceIDPattern         = regexp.MustCompile(`^[a-z0-9]+(?:[-_][a-z0-9]+)*$`)
	serviceReferencePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	typeIDPattern           = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	numericVersionPattern   = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){0,2}$`)
)

const (
	sourceTransportFSC       = "fsc"
	sourceTransportUnsecured = "unsecured"
)

type sourceMetadataEndpoint struct {
	Transport        string `json:"transport" yaml:"transport"`
	ServiceReference string `json:"service_reference,omitempty" yaml:"service_reference,omitempty"`
	Path             string `json:"path,omitempty" yaml:"path,omitempty"`
	Endpoint         string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	GrantHash        string `json:"grant_hash,omitempty" yaml:"grant_hash,omitempty"`
}

type sourceDataAccess struct {
	Transport        string `json:"transport" yaml:"transport"`
	ServiceReference string `json:"service_reference,omitempty" yaml:"service_reference,omitempty"`
	GrantHash        string `json:"grant_hash,omitempty" yaml:"grant_hash,omitempty"`
}

type sourceRegistration struct {
	SourceID         string                 `json:"source_id,omitempty" yaml:"source_id,omitempty"`
	ProviderPeerID   string                 `json:"provider_peer_id,omitempty" yaml:"provider_peer_id,omitempty"`
	SourceOIN        string                 `json:"source_oin" yaml:"source_oin"`
	Name             string                 `json:"name" yaml:"name"`
	CertificateSet   string                 `json:"certificate_set,omitempty" yaml:"certificate_set,omitempty"`
	MetadataEndpoint sourceMetadataEndpoint `json:"metadata_endpoint" yaml:"metadata_endpoint"`
	DataAccess       sourceDataAccess       `json:"data_access" yaml:"data_access"`
	// Logo is certificate-provisioning input, not source-published metadata.
	Logo *organizationLogo `json:"-" yaml:"-"`
}

func (r sourceRegistration) certificateSetID() string {
	if r.CertificateSet != "" {
		return r.CertificateSet
	}
	return r.SourceOIN
}

type validatedSourceRegistration struct {
	Registration sourceRegistration
	Document     sourceMetadataDocument
	Payload      []byte
	MetadataURL  string
	Publications []*typeMetadataPublication
	ValidatedAt  time.Time
	ExpiresAt    time.Time
	MetadataETag string
}

func (r sourceRegistration) validate() error {
	if r.SourceID != "" && !sourceIDPattern.MatchString(r.SourceID) {
		return fmt.Errorf("source registration source_id is invalid")
	}
	if !sourceOINPattern.MatchString(r.SourceOIN) {
		return fmt.Errorf("source registration source_oin must contain exactly 20 digits")
	}
	if r.MetadataEndpoint.Transport == sourceTransportFSC {
		if !peerIDPattern.MatchString(r.ProviderPeerID) {
			return fmt.Errorf("source registration provider_peer_id must contain exactly 20 alphanumeric characters")
		}
	} else if r.ProviderPeerID != "" {
		return fmt.Errorf("source registration provider_peer_id is only allowed for FSC transport")
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("source registration name is required")
	}
	if r.CertificateSet != "" && !sourceIDPattern.MatchString(r.CertificateSet) {
		return fmt.Errorf("source registration certificate_set is invalid")
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
		if e.GrantHash == "" {
			return fmt.Errorf("grant_hash is required for FSC transport")
		}
	case sourceTransportUnsecured:
		if e.ServiceReference != "" || e.Path != "" {
			return fmt.Errorf("service_reference and path are not allowed for unsecured transport")
		}
		if err := validateAbsoluteUnsecuredEndpoint(e.Endpoint); err != nil {
			return fmt.Errorf("endpoint: %w", err)
		}
		if e.GrantHash != "" {
			return fmt.Errorf("grant_hash is not allowed for unsecured transport")
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
		if a.GrantHash == "" {
			return fmt.Errorf("grant_hash is required for FSC transport")
		}
	case sourceTransportUnsecured:
		if a.ServiceReference != "" {
			return fmt.Errorf("service_reference is not allowed for unsecured transport")
		}
		if a.GrantHash != "" {
			return fmt.Errorf("grant_hash is not allowed for unsecured transport")
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

func validateAbsoluteUnsecuredEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	return nil
}

func validateSourcePayload(registration sourceRegistration, payload []byte, metadataURL, schemaPath, publicBaseURL string, now time.Time) (*validatedSourceRegistration, error) {
	if err := validateTypeMetadataBaseURL(publicBaseURL); err != nil {
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
	if document.Capabilities.EUDI == nil || document.Capabilities.EUDI.Version != "1.0" {
		return nil, fmt.Errorf("source metadata has no supported EUDI capability")
	}
	attestations := document.eudiAttestations()
	if len(attestations) == 0 {
		return nil, fmt.Errorf("source metadata EUDI capability must contain at least one attestation")
	}
	if _, err := compareNumericVersion(document.Version, "0"); err != nil {
		return nil, fmt.Errorf("source metadata version: %w", err)
	}
	seenTypes := make(map[string]bool, len(attestations))
	publications := make([]*typeMetadataPublication, 0, len(attestations))
	var expiresAt time.Time
	for _, definition := range attestations {
		if seenTypes[definition.TypeID] {
			return nil, fmt.Errorf("source metadata contains duplicate type_id %q", definition.TypeID)
		}
		seenTypes[definition.TypeID] = true
		if err := validateSourceAttestation(definition); err != nil {
			return nil, fmt.Errorf("attestation %q: %w", definition.TypeID, err)
		}
		if err := validateGraphQLEndpoint(definition.GraphQL, registration.DataAccess.Transport); err != nil {
			return nil, fmt.Errorf("attestation %q graphql.endpoint: %w", definition.TypeID, err)
		}
		validatedExpiry, err := validateSourceMetadataEnvelope(document, definition, now, defaultSourceMetadataCachePolicy)
		if err != nil {
			return nil, err
		}
		expiresAt = validatedExpiry
		publication, err := newTypeMetadataPublication(publicBaseURL, registration.SourceID, definition)
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
		ValidatedAt:  now.UTC(),
		ExpiresAt:    expiresAt.UTC(),
	}, nil
}

type sourceMetadataFetch struct {
	Payload     []byte
	ETag        string
	NotModified bool
}

func fetchSourceMetadata(ctx context.Context, client *http.Client, metadataURL, transport, grantHash string) ([]byte, error) {
	fetched, err := fetchSourceMetadataCandidate(ctx, client, metadataURL, transport, grantHash, "")
	if err != nil {
		return nil, err
	}
	if fetched.NotModified {
		return nil, fmt.Errorf("source returned not modified for an unconditional metadata request")
	}
	return fetched.Payload, nil
}

func fetchSourceMetadataCandidate(ctx context.Context, client *http.Client, metadataURL, transport, grantHash, etag string) (sourceMetadataFetch, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return sourceMetadataFetch{}, fmt.Errorf("create source metadata validation request: %w", err)
	}
	req.Header.Set("Accept", sourceMetadataMediaType)
	switch transport {
	case sourceTransportFSC:
		txID, err := newFscTransactionID()
		if err != nil {
			return sourceMetadataFetch{}, fmt.Errorf("generate source metadata Fsc-Transaction-Id: %w", err)
		}
		req.Header.Set("Fsc-Transaction-Id", txID)
		req.Header.Set("Fsc-Grant-Hash", grantHash)
	case sourceTransportUnsecured:
		// This profile deliberately adds no transport authentication headers.
	default:
		return sourceMetadataFetch{}, fmt.Errorf("source metadata transport %q is configured but not implemented", transport)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := doSourceRequest(client, req, transport == sourceTransportUnsecured)
	if err != nil {
		return sourceMetadataFetch{}, fmt.Errorf("fetch source metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
		if resp.StatusCode == http.StatusNotModified && etag != "" {
			return sourceMetadataFetch{ETag: etag, NotModified: true}, nil
		}
		return sourceMetadataFetch{}, fmt.Errorf("source metadata redirect is not allowed for %s transport", transport)
	}
	if resp.StatusCode != http.StatusOK {
		return sourceMetadataFetch{}, fmt.Errorf("source metadata request returned status %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != sourceMetadataMediaType {
		return sourceMetadataFetch{}, fmt.Errorf("source metadata content type must be %s", sourceMetadataMediaType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return sourceMetadataFetch{}, fmt.Errorf("read source metadata: %w", err)
	}
	return sourceMetadataFetch{Payload: body, ETag: resp.Header.Get("ETag")}, nil
}

func doSourceRequest(client *http.Client, request *http.Request, rejectRedirects bool) (*http.Response, error) {
	if !rejectRedirects {
		return client.Do(request)
	}
	withoutRedirects := *client
	withoutRedirects.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return withoutRedirects.Do(request)
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
		return fmt.Errorf("source metadata does not match gbo-source-metadata-v1: %w", err)
	}
	return nil
}
