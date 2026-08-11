package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gbo-demo/eudi-adapter/internal/gbosimplev1"

	"github.com/graphql-go/graphql/language/ast"
	"github.com/graphql-go/graphql/language/parser"
	"github.com/graphql-go/graphql/language/source"
)

const sourceMetadataMediaType = "application/jose"

var sourceMetadataBase64URL = base64.RawURLEncoding

// sourceMetadataConfig is the deliberately small phase-1 registration. A
// later phase replaces these direct settings with the normal source
// registration, but the trust decisions are already explicit here.
type sourceMetadataConfig struct {
	URL         string
	ExpectedOIN string
	PublicJWK   json.RawMessage
	TypeID      string
}

type sourceMetadataDocument struct {
	SchemaVersion string                        `json:"schema_version"`
	SourceOIN     string                        `json:"source_oin"`
	Version       string                        `json:"version"`
	IssuedAt      string                        `json:"issued_at"`
	ExpiresAt     string                        `json:"expires_at"`
	Attestations  []sourceAttestationDefinition `json:"attestations"`
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
	Document        string                     `json:"document"`
	SubjectVariable string                     `json:"subject_variable"`
	Parameters      map[string]sourceParameter `json:"parameters"`
	ResultPointer   string                     `json:"result_pointer"`
	Cardinality     string                     `json:"cardinality"`
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

type sourceMetadataJWK struct {
	KTY string `json:"kty"`
	CRV string `json:"crv"`
	X   string `json:"x"`
}

type sourceMetadataJWSHeader struct {
	Algorithm string             `json:"alg"`
	KeyID     string             `json:"kid"`
	Type      string             `json:"typ"`
	Critical  []string           `json:"crit,omitempty"`
	JWK       *sourceMetadataJWK `json:"jwk,omitempty"`
}

// loadSourceMetadata fetches the declaration through FSC, verifies it against
// the pinned source key and OIN, validates it and selects the activated type.
func loadSourceMetadata(ctx context.Context, client *http.Client, cfg sourceMetadataConfig) (*activeSourceMetadata, error) {
	if cfg.URL == "" || cfg.ExpectedOIN == "" || len(cfg.PublicJWK) == 0 || cfg.TypeID == "" {
		return nil, fmt.Errorf("source metadata registration is incomplete")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("create source metadata request: %w", err)
	}
	fscTxID, err := newFscTransactionID()
	if err != nil {
		return nil, fmt.Errorf("generate source metadata Fsc-Transaction-Id: %w", err)
	}
	req.Header.Set("Accept", sourceMetadataMediaType)
	req.Header.Set("Fsc-Transaction-Id", fscTxID)
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
	compactJWS, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read source metadata: %w", err)
	}
	payload, err := verifySourceMetadataJWS(strings.TrimSpace(string(compactJWS)), cfg.PublicJWK)
	if err != nil {
		return nil, err
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
	for _, definition := range document.Attestations {
		if definition.TypeID != cfg.TypeID {
			continue
		}
		if err := validateSourceAttestation(definition); err != nil {
			return nil, sourceMetadataDocument{}, fmt.Errorf("attestation %q: %w", cfg.TypeID, err)
		}
		return &activeSourceMetadata{
			Version: document.Version, SourceOIN: cfg.ExpectedOIN, TypeID: cfg.TypeID, Definition: definition,
		}, document, nil
	}
	return nil, sourceMetadataDocument{}, fmt.Errorf("source metadata has no attestation %q", cfg.TypeID)
}

func verifySourceMetadataJWS(compact string, rawJWK json.RawMessage) ([]byte, error) {
	var jwk sourceMetadataJWK
	if err := json.Unmarshal(rawJWK, &jwk); err != nil {
		return nil, fmt.Errorf("parse source metadata JWK: %w", err)
	}
	if jwk.KTY != "OKP" || jwk.CRV != "Ed25519" {
		return nil, fmt.Errorf("source metadata JWK must be an Ed25519 OKP key")
	}
	publicKey, err := sourceMetadataBase64URL.DecodeString(jwk.X)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("source metadata JWK has an invalid public key")
	}

	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("source metadata is not a compact JWS")
	}
	protected, err := sourceMetadataBase64URL.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode source metadata JWS header: %w", err)
	}
	var header sourceMetadataJWSHeader
	if err := json.Unmarshal(protected, &header); err != nil {
		return nil, fmt.Errorf("parse source metadata JWS header: %w", err)
	}
	if header.Algorithm != "EdDSA" || header.Type != "gbo-attestations+jws" {
		return nil, fmt.Errorf("source metadata JWS has unsupported protected headers")
	}
	if len(header.Critical) > 0 {
		return nil, fmt.Errorf("source metadata JWS contains an unsupported critical JWS header")
	}
	if header.KeyID != sourceMetadataJWKThumbprint(jwk) {
		return nil, fmt.Errorf("source metadata JWS key id does not match the pinned key")
	}
	signature, err := sourceMetadataBase64URL.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decode source metadata JWS signature: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), []byte(parts[0]+"."+parts[1]), signature) {
		return nil, fmt.Errorf("verify source metadata JWS signature: invalid signature")
	}
	payload, err := sourceMetadataBase64URL.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode source metadata JWS payload: %w", err)
	}
	return payload, nil
}

func sourceMetadataJWKThumbprint(jwk sourceMetadataJWK) string {
	canonical := fmt.Sprintf(`{"crv":"%s","kty":"%s","x":"%s"}`, jwk.CRV, jwk.KTY, jwk.X)
	digest := sha256.Sum256([]byte(canonical))
	return sourceMetadataBase64URL.EncodeToString(digest[:])
}

func verifySourceMetadataJWSWithThumbprint(compact, expectedThumbprint string) ([]byte, json.RawMessage, error) {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return nil, nil, fmt.Errorf("source metadata is not a compact JWS")
	}
	protected, err := sourceMetadataBase64URL.DecodeString(parts[0])
	if err != nil {
		return nil, nil, fmt.Errorf("decode source metadata JWS header: %w", err)
	}
	var header sourceMetadataJWSHeader
	if err := json.Unmarshal(protected, &header); err != nil {
		return nil, nil, fmt.Errorf("parse source metadata JWS header: %w", err)
	}
	if header.JWK == nil {
		return nil, nil, fmt.Errorf("source metadata JWS header has no public jwk")
	}
	actual := sourceMetadataJWKThumbprint(*header.JWK)
	want := strings.TrimPrefix(expectedThumbprint, "sha256-")
	if want == "" || actual != want {
		return nil, nil, fmt.Errorf("source metadata JWK thumbprint does not match the registered thumbprint")
	}
	rawJWK, err := json.Marshal(header.JWK)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal source metadata public JWK: %w", err)
	}
	payload, err := verifySourceMetadataJWS(compact, rawJWK)
	if err != nil {
		return nil, nil, err
	}
	return payload, rawJWK, nil
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
	return sourceQueryPlan{Query: s.Definition.GraphQL.Document, Variables: variables}, nil
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
