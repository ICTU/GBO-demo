package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	sourceOINPattern        = regexp.MustCompile(`^[0-9]{20}$`)
	serviceReferencePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

type sourceRegistration struct {
	SourceOIN                    string `json:"source_oin"`
	Name                         string `json:"name"`
	MetadataFSCServiceReference  string `json:"metadata_fsc_service_reference"`
	DataFSCServiceReference      string `json:"data_fsc_service_reference"`
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
	values, err := parseSourceRegistrationYAML(raw)
	if err != nil {
		return sourceRegistration{}, err
	}
	registration := sourceRegistration{
		SourceOIN:                    values["source_oin"],
		Name:                         values["name"],
		MetadataFSCServiceReference:  values["metadata_fsc_service_reference"],
		DataFSCServiceReference:      values["data_fsc_service_reference"],
	}
	if err := registration.validate(); err != nil {
		return sourceRegistration{}, err
	}
	if err := validateSourceRegistrationUniqueness(path, registration); err != nil {
		return sourceRegistration{}, err
	}
	return registration, nil
}

func parseSourceRegistrationYAML(raw []byte) (map[string]string, error) {
	allowed := map[string]bool{
		"source_oin": true, "name": true, "metadata_fsc_service_reference": true,
		"data_fsc_service_reference": true,
	}
	values := make(map[string]string, len(allowed))
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(scanner.Text(), " ") || strings.HasPrefix(scanner.Text(), "\t") {
			return nil, fmt.Errorf("source registration line %d: nested YAML is not supported", lineNumber)
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("source registration line %d: expected key: value", lineNumber)
		}
		key := strings.TrimSpace(parts[0])
		if !allowed[key] {
			return nil, fmt.Errorf("source registration line %d: unknown field %q", lineNumber, key)
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("source registration line %d: duplicate field %q", lineNumber, key)
		}
		value, err := parseSourceRegistrationScalar(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("source registration line %d field %q: %w", lineNumber, key, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan source registration: %w", err)
	}
	for key := range allowed {
		if values[key] == "" {
			return nil, fmt.Errorf("source registration field %q is required", key)
		}
	}
	return values, nil
}

func parseSourceRegistrationScalar(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("value is empty")
	}
	if strings.HasPrefix(value, `"`) {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value: %w", err)
		}
		return decoded, nil
	}
	if strings.HasPrefix(value, "'") {
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", fmt.Errorf("invalid quoted value")
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	}
	if strings.Contains(value, " #") {
		value = strings.TrimSpace(strings.SplitN(value, " #", 2)[0])
	}
	return value, nil
}

func (r sourceRegistration) validate() error {
	if !sourceOINPattern.MatchString(r.SourceOIN) {
		return fmt.Errorf("source registration source_oin must contain exactly 20 digits")
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("source registration name is required")
	}
	if !serviceReferencePattern.MatchString(r.MetadataFSCServiceReference) {
		return fmt.Errorf("source registration metadata FSC service reference is invalid")
	}
	if !serviceReferencePattern.MatchString(r.DataFSCServiceReference) {
		return fmt.Errorf("source registration data FSC service reference is invalid")
	}
	if r.MetadataFSCServiceReference == r.DataFSCServiceReference {
		return fmt.Errorf("source registration metadata and data FSC services must be separate")
	}
	return nil
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
		values, err := parseSourceRegistrationYAML(raw)
		if err != nil {
			return fmt.Errorf("parse source registration %q: %w", candidate, err)
		}
		if values["source_oin"] == registration.SourceOIN {
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
	metadataURL := strings.TrimRight(outwayURL, "/") + "/" + registration.MetadataFSCServiceReference + "/.well-known/gbo-attestations"
	payload, err := fetchSourceMetadata(ctx, client, metadataURL)
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

func fetchSourceMetadata(ctx context.Context, client *http.Client, metadataURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create source metadata validation request: %w", err)
	}
	txID, err := newFscTransactionID()
	if err != nil {
		return nil, fmt.Errorf("generate source metadata Fsc-Transaction-Id: %w", err)
	}
	req.Header.Set("Accept", sourceMetadataMediaType)
	req.Header.Set("Fsc-Transaction-Id", txID)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch source metadata through FSC: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source metadata FSC request returned status %d", resp.StatusCode)
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
