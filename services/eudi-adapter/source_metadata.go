package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"gbo-demo/eudi-adapter/internal/gbosimplev1"

	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/graphql/language/parser"
	"github.com/graphql-go/graphql/language/source"
)

const sourceMetadataMediaType = "application/json"

// sourceMetadataConfig is resolved from an active onboarding record. Endpoint
// and transport choices are deliberately not separate deployment settings.
type sourceMetadataConfig struct {
	URL               string
	MetadataTransport string
	MetadataGrantHash string
	DataTransport     string
	ExpectedOIN       string
	TypeID            string
}

type sourceMetadataDocument struct {
	SchemaVersion string                     `json:"schema_version"`
	SourceOIN     string                     `json:"source_oin"`
	Version       string                     `json:"version"`
	IssuedAt      string                     `json:"issued_at"`
	ExpiresAt     string                     `json:"expires_at"`
	Capabilities  sourceMetadataCapabilities `json:"capabilities"`
}

type sourceMetadataCapabilities struct {
	EUDI *sourceEUDICapability `json:"eudi,omitempty"`
}

type sourceEUDICapability struct {
	Version      string                        `json:"version"`
	Attestations []sourceAttestationDefinition `json:"attestations"`
}

func (d sourceMetadataDocument) eudiAttestations() []sourceAttestationDefinition {
	if d.Capabilities.EUDI == nil {
		return nil
	}
	return d.Capabilities.EUDI.Attestations
}

type sourceAttestationDefinition struct {
	TypeID          string                           `json:"type_id"`
	TypeVersion     string                           `json:"type_version"`
	GraphQL         sourceGraphQL                    `json:"graphql"`
	MappingProfile  string                           `json:"mapping_profile"`
	Mapping         gbosimplev1.Mapping              `json:"mapping"`
	AttributeSchema map[string]sourceAttributeSchema `json:"attribute_schema"`
	TypeMetadata    json.RawMessage                  `json:"type_metadata"`
}

type sourceGraphQL struct {
	ServiceReference string                     `json:"service_reference,omitempty"`
	Endpoint         string                     `json:"endpoint"`
	Document         string                     `json:"document"`
	SubjectVariable  string                     `json:"subject_variable"`
	Parameters       map[string]sourceParameter `json:"parameters"`
	ResultPointer    string                     `json:"result_pointer"`
	Cardinality      string                     `json:"cardinality"`
}

type sourceParameter struct {
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

type sourceAttributeSchema struct {
	Type   string `json:"type"`
	Format string `json:"format,omitempty"`
	Unit   string `json:"unit,omitempty"`
}

func (a *sourceAttributeSchema) UnmarshalJSON(data []byte) error {
	type plainAttributeSchema sourceAttributeSchema
	var decoded plainAttributeSchema
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("invalid attribute_schema rule: %w", err)
	}
	*a = sourceAttributeSchema(decoded)
	return nil
}

type mappingRule = gbosimplev1.Rule

type activeSourceMetadata struct {
	Version      string
	SourceOIN    string
	TypeID       string
	Definition   sourceAttestationDefinition
	VCT          string
	VCTIntegrity string
	CacheState   string
}

type sourceMetadataRuntime interface {
	current(now time.Time) (*activeSourceMetadata, error)
}

// loadSourceMetadata fetches the declaration through the onboarded transport,
// validates its OIN and selects the activated type.
func loadSourceMetadata(ctx context.Context, client *http.Client, cfg sourceMetadataConfig) (*activeSourceMetadata, error) {
	if cfg.URL == "" || cfg.ExpectedOIN == "" || cfg.TypeID == "" {
		return nil, fmt.Errorf("source metadata registration is incomplete")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("create source metadata request: %w", err)
	}
	req.Header.Set("Accept", sourceMetadataMediaType)
	if cfg.MetadataTransport == sourceTransportFSC {
		fscTxID, err := newFscTransactionID()
		if err != nil {
			return nil, fmt.Errorf("generate source metadata Fsc-Transaction-Id: %w", err)
		}
		req.Header.Set("Fsc-Transaction-Id", fscTxID)
		req.Header.Set("Fsc-Grant-Hash", cfg.MetadataGrantHash)
	} else {
		return nil, fmt.Errorf("source metadata transport %q is configured but not implemented", cfg.MetadataTransport)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch source metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source metadata status %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != sourceMetadataMediaType {
		return nil, fmt.Errorf("source metadata content type must be %s", sourceMetadataMediaType)
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read source metadata: %w", err)
	}

	metadata, _, err := parseSourceMetadataPayload(payload, cfg)
	return metadata, err
}

func parseSourceMetadataPayload(payload []byte, cfg sourceMetadataConfig) (*activeSourceMetadata, sourceMetadataDocument, error) {
	var document sourceMetadataDocument
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, sourceMetadataDocument{}, fmt.Errorf("parse source metadata payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, sourceMetadataDocument{}, fmt.Errorf("parse source metadata payload: trailing JSON data")
	}
	if document.SourceOIN != cfg.ExpectedOIN {
		return nil, sourceMetadataDocument{}, fmt.Errorf("source metadata OIN %q does not match registered OIN %q", document.SourceOIN, cfg.ExpectedOIN)
	}
	if document.SchemaVersion != "1.0" {
		return nil, sourceMetadataDocument{}, fmt.Errorf("unsupported source metadata schema_version %q", document.SchemaVersion)
	}
	if document.Capabilities.EUDI == nil || document.Capabilities.EUDI.Version != "1.0" {
		return nil, sourceMetadataDocument{}, fmt.Errorf("source metadata has no supported EUDI capability")
	}
	for _, definition := range document.eudiAttestations() {
		if definition.TypeID != cfg.TypeID {
			continue
		}
		if err := validateSourceAttestation(definition); err != nil {
			return nil, sourceMetadataDocument{}, fmt.Errorf("attestation %q: %w", cfg.TypeID, err)
		}
		if err := validateGraphQLEndpoint(definition.GraphQL, cfg.DataTransport); err != nil {
			return nil, sourceMetadataDocument{}, fmt.Errorf("attestation %q graphql.endpoint: %w", cfg.TypeID, err)
		}
		return &activeSourceMetadata{
			Version: document.Version, SourceOIN: cfg.ExpectedOIN, TypeID: cfg.TypeID, Definition: definition,
		}, document, nil
	}
	return nil, sourceMetadataDocument{}, fmt.Errorf("source metadata has no attestation %q", cfg.TypeID)
}

func validateSourceAttestation(definition sourceAttestationDefinition) error {
	if definition.TypeVersion == "" {
		return fmt.Errorf("type_version is required")
	}
	if definition.GraphQL.SubjectVariable == "" {
		return fmt.Errorf("graphql.subject_variable is required")
	}
	if definition.GraphQL.ResultPointer == "" {
		return fmt.Errorf("graphql.result_pointer is required")
	}
	if definition.GraphQL.Cardinality != "exactly_one" {
		return fmt.Errorf("phase 1 only supports cardinality exactly_one")
	}
	if definition.MappingProfile != "gbo-simple-v1" {
		return fmt.Errorf("unsupported mapping_profile %q", definition.MappingProfile)
	}
	if len(definition.Mapping) == 0 {
		return fmt.Errorf("mapping must contain at least one claim")
	}
	if err := gbosimplev1.Validate(definition.Mapping); err != nil {
		return err
	}
	if err := validateAttributeSchema(definition); err != nil {
		return err
	}
	document, err := parser.Parse(parser.ParseParams{Source: source.NewSource(&source.Source{Body: []byte(definition.GraphQL.Document), Name: "source-metadata.graphql"})})
	if err != nil {
		return fmt.Errorf("invalid GraphQL document: %w", err)
	}
	operationCount := 0
	for _, node := range document.Definitions {
		if operation, ok := node.(*ast.OperationDefinition); ok {
			operationCount++
			if operation.Operation != ast.OperationTypeQuery {
				return fmt.Errorf("GraphQL document must contain a query operation")
			}
		}
	}
	if operationCount != 1 {
		return fmt.Errorf("GraphQL document must contain exactly one query operation")
	}
	return nil
}

func validateGraphQLEndpoint(graphql sourceGraphQL, transport string) error {
	switch transport {
	case sourceTransportFSC:
		if !serviceReferencePattern.MatchString(graphql.ServiceReference) {
			return fmt.Errorf("service_reference is required and must be valid for FSC transport")
		}
		return validateAbsoluteURLPath(graphql.Endpoint)
	case sourceTransportHTTPSMTLS:
		if graphql.ServiceReference != "" {
			return fmt.Errorf("service_reference is not allowed for https-mtls transport")
		}
		return validateAbsoluteHTTPSEndpoint(graphql.Endpoint)
	default:
		return fmt.Errorf("unsupported data transport %q", transport)
	}
}

func validateAttributeSchema(definition sourceAttestationDefinition) error {
	if len(definition.AttributeSchema) != len(definition.Mapping) {
		return fmt.Errorf("attribute_schema must define exactly the mapped claims")
	}
	for claim, rule := range definition.Mapping {
		attribute, ok := definition.AttributeSchema[claim]
		if !ok {
			return fmt.Errorf("attribute_schema is missing mapped claim %q", claim)
		}
		wantType, wantFormat := rule.Datatype, ""
		switch rule.Datatype {
		case "date":
			wantType, wantFormat = "string", "date"
		case "gYear":
			wantType, wantFormat = "integer", "gYear"
		}
		if attribute.Type != wantType || attribute.Format != wantFormat {
			return fmt.Errorf("attribute_schema claim %q must have type %q and format %q", claim, wantType, wantFormat)
		}
		if attribute.Unit != "" {
			if attribute.Type != "number" {
				return fmt.Errorf("attribute_schema claim %q can only declare a unit for type number", claim)
			}
			if !isISO4217Alpha3(attribute.Unit) {
				return fmt.Errorf("attribute_schema claim %q unit must be an ISO 4217 alpha-3 code", claim)
			}
		}
	}
	return nil
}

func isISO4217Alpha3(unit string) bool {
	if len(unit) != 3 {
		return false
	}
	for i := 0; i < len(unit); i++ {
		if unit[i] < 'A' || unit[i] > 'Z' {
			return false
		}
	}
	return true
}

func (s *activeSourceMetadata) current(_ time.Time) (*activeSourceMetadata, error) {
	return s, nil
}

func (s *activeSourceMetadata) queryPlan(bsn string, supplied map[string][]string) (sourceQueryPlan, error) {
	variables := map[string]any{s.Definition.GraphQL.SubjectVariable: bsn}
	for name, parameter := range s.Definition.GraphQL.Parameters {
		values, configured := supplied[name]
		if configured && len(values) != 1 {
			return sourceQueryPlan{}, fmt.Errorf("source parameter %q must occur exactly once", name)
		}
		if configured {
			value, err := parseSourceParameter(name, parameter.Type, values[0])
			if err != nil {
				return sourceQueryPlan{}, err
			}
			variables[name] = value
		} else if parameter.Required {
			return sourceQueryPlan{}, fmt.Errorf("no runtime value for required source parameter %q", name)
		}
	}
	for name := range supplied {
		if _, declared := s.Definition.GraphQL.Parameters[name]; !declared {
			return sourceQueryPlan{}, fmt.Errorf("request supplies undeclared source parameter %q", name)
		}
	}
	return sourceQueryPlan{ServiceReference: s.Definition.GraphQL.ServiceReference, Endpoint: s.Definition.GraphQL.Endpoint, Query: s.Definition.GraphQL.Document, Variables: variables}, nil
}

func parseSourceParameter(name, parameterType, raw string) (any, error) {
	switch parameterType {
	case "string":
		return raw, nil
	case "integer":
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("source parameter %q must be an integer", name)
		}
		return value, nil
	case "number":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("source parameter %q must be a number", name)
		}
		return value, nil
	case "boolean":
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("source parameter %q must be a boolean", name)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("source parameter %q has unsupported type %q", name, parameterType)
	}
}

func (s *activeSourceMetadata) project(rawGraphQL []byte) (gbosimplev1.Projection, error) {
	root, err := gbosimplev1.DecodeJSON(rawGraphQL)
	if err != nil {
		return gbosimplev1.Projection{}, fmt.Errorf("decode GraphQL response for source projection: %w", err)
	}
	projection, err := gbosimplev1.Project(root, s.Definition.GraphQL.ResultPointer, s.Definition.GraphQL.Cardinality, s.Definition.Mapping)
	if err != nil {
		return gbosimplev1.Projection{}, fmt.Errorf("project source response: %w", err)
	}
	return projection, nil
}
