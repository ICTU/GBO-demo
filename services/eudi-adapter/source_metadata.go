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
	"os"
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

type sourceMetadataShadow struct {
	Version      string
	UsecaseKey   string
	Definition   sourceAttestationDefinition
	VCT          string
	VCTIntegrity string
}

type sourceMetadataRuntime interface {
	appliesTo(usecaseKey string, uc Usecase) bool
	current(now time.Time) (*sourceMetadataShadow, error)
}

type sourceMetadataJWK struct {
	KTY string `json:"kty"`
	CRV string `json:"crv"`
	X   string `json:"x"`
}

type sourceMetadataJWSHeader struct {
	Algorithm string   `json:"alg"`
	KeyID     string   `json:"kid"`
	Type      string   `json:"typ"`
	Critical  []string `json:"crit,omitempty"`
}

// phase1ExpectedIncomeClaims is the migration baseline, deliberately kept
// outside source-controlled metadata. It disappears with the legacy formatter
// after parity has been proven; until then it prevents the source from making
// a claim vanish from both its query and mapping while still reporting match.
var phase1ExpectedIncomeClaims = map[string]struct{}{
	"belastingjaar":   {},
	"verzamelinkomen": {},
	"aangifte_status": {},
	"indieningsdatum": {},
	"inkomen_box1":    {},
	"inkomen_box2":    {},
	"inkomen_box3":    {},
}

func loadConfiguredSourceMetadataShadow(ctx context.Context, client *http.Client, cfg config) (*sourceMetadataShadow, error) {
	if !cfg.SourceMetadataShadowEnabled {
		return nil, nil
	}
	if cfg.SourceMetadataPublicJWKPath == "" {
		return nil, fmt.Errorf("SOURCE_METADATA_PUBLIC_JWK_PATH is required when shadow mode is enabled")
	}
	if !strings.HasPrefix(cfg.SourceMetadataOutwayPath, "/") || strings.HasPrefix(cfg.SourceMetadataOutwayPath, "//") {
		return nil, fmt.Errorf("SOURCE_METADATA_OUTWAY_PATH must be an absolute path on the configured FSC Outway")
	}
	if cfg.SourceMetadataUsecaseKey == "" {
		return nil, fmt.Errorf("SOURCE_METADATA_USECASE_KEY is required when shadow mode is enabled")
	}
	publicJWK, err := os.ReadFile(cfg.SourceMetadataPublicJWKPath)
	if err != nil {
		return nil, fmt.Errorf("read source metadata public JWK: %w", err)
	}
	shadow, err := loadSourceMetadataShadow(ctx, client, sourceMetadataConfig{
		URL:         strings.TrimRight(cfg.OutwayURL, "/") + cfg.SourceMetadataOutwayPath,
		ExpectedOIN: cfg.SourceMetadataOIN,
		PublicJWK:   publicJWK,
		TypeID:      cfg.SourceMetadataTypeID,
	})
	if err != nil {
		return nil, err
	}
	shadow.UsecaseKey = cfg.SourceMetadataUsecaseKey
	return shadow, nil
}

// loadSourceMetadataShadow performs the phase-1 onboarding step: fetch the
// declaration, verify it against the pinned source key and OIN, validate its
// GraphQL syntax, and keep only the requested attestation definition.
func loadSourceMetadataShadow(ctx context.Context, client *http.Client, cfg sourceMetadataConfig) (*sourceMetadataShadow, error) {
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

	shadow, _, err := parseSourceMetadataPayload(payload, cfg)
	return shadow, err
}

func parseSourceMetadataPayload(payload []byte, cfg sourceMetadataConfig) (*sourceMetadataShadow, sourceMetadataDocument, error) {
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
		return &sourceMetadataShadow{Version: document.Version, Definition: definition}, document, nil
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

func (s *sourceMetadataShadow) appliesTo(usecaseKey string, uc Usecase) bool {
	return s != nil && s.UsecaseKey == usecaseKey && uc.bron() == bronBD && len(uc.Belastingjaren) == 1
}

func (s *sourceMetadataShadow) current(_ time.Time) (*sourceMetadataShadow, error) {
	return s, nil
}

func (s *sourceMetadataShadow) queryPlan(usecaseKey, bsn string, uc Usecase) (sourceQueryPlan, error) {
	if !s.appliesTo(usecaseKey, uc) {
		return sourceQueryPlan{}, fmt.Errorf("source metadata does not apply to this usecase")
	}
	variables := map[string]any{s.Definition.GraphQL.SubjectVariable: bsn}
	for name, parameter := range s.Definition.GraphQL.Parameters {
		switch name {
		case "jaar":
			variables[name] = uc.Belastingjaren[0]
		default:
			if parameter.Required {
				return sourceQueryPlan{}, fmt.Errorf("no runtime value for required source parameter %q", name)
			}
		}
	}
	return sourceQueryPlan{Query: s.Definition.GraphQL.Document, Variables: variables}, nil
}

// compareLegacy projects only the claims declared by the source and checks
// those against the legacy formatter. Legacy-only presentation claims (such
// as verklaring_tekst) intentionally do not become mapping capabilities.
func (s *sourceMetadataShadow) compareLegacy(rawGraphQL []byte, docs []attestation) (bool, error) {
	if len(docs) != 1 {
		return false, fmt.Errorf("shadow comparison expected one legacy document")
	}
	for claim := range phase1ExpectedIncomeClaims {
		if _, declared := s.Definition.Mapping[claim]; !declared {
			return false, nil
		}
		if _, issued := docs[0].Attributes[claim]; !issued {
			return false, nil
		}
	}
	// verklaring_tekst is presentation text produced only by the legacy
	// formatter and is deliberately absent from the source mapping. Every
	// other issued claim must be declared: otherwise removing a field from
	// both the source query and mapping would incorrectly report a match.
	for claim := range docs[0].Attributes {
		if claim == "verklaring_tekst" {
			continue
		}
		if _, declared := s.Definition.Mapping[claim]; !declared {
			return false, nil
		}
	}
	root, err := gbosimplev1.DecodeJSON(rawGraphQL)
	if err != nil {
		return false, fmt.Errorf("decode GraphQL response for shadow projection: %w", err)
	}
	projection, err := gbosimplev1.Project(root, s.Definition.GraphQL.ResultPointer, s.Definition.GraphQL.Cardinality, s.Definition.Mapping)
	if err != nil {
		return false, fmt.Errorf("project source response: %w", err)
	}
	if projection.Outcome != gbosimplev1.OutcomeCredential {
		return false, fmt.Errorf("project source response: %s", projection.Outcome)
	}
	for claim, converted := range projection.Claims {
		if !gbosimplev1.EqualJSON(converted, docs[0].Attributes[claim]) {
			return false, nil
		}
	}
	return true, nil
}
