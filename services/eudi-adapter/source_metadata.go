package main

import (
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
	"reflect"
	"strconv"
	"strings"

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
	SourceOIN    string                        `json:"source_oin"`
	Version      string                        `json:"version"`
	IssuedAt     string                        `json:"issued_at"`
	ExpiresAt    string                        `json:"expires_at"`
	Attestations []sourceAttestationDefinition `json:"attestations"`
}

type sourceAttestationDefinition struct {
	TypeID         string                 `json:"type_id"`
	TypeVersion    string                 `json:"type_version"`
	GraphQL        sourceGraphQL          `json:"graphql"`
	MappingProfile string                 `json:"mapping_profile"`
	Mapping        map[string]mappingRule `json:"mapping"`
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

type mappingRule struct {
	Pointer  string `json:"pointer"`
	Datatype string `json:"datatype"`
}

type sourceMetadataShadow struct {
	Version    string
	Definition sourceAttestationDefinition
}

type sourceMetadataJWK struct {
	KTY string `json:"kty"`
	CRV string `json:"crv"`
	X   string `json:"x"`
}

type sourceMetadataJWSHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
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
	publicJWK, err := os.ReadFile(cfg.SourceMetadataPublicJWKPath)
	if err != nil {
		return nil, fmt.Errorf("read source metadata public JWK: %w", err)
	}
	return loadSourceMetadataShadow(ctx, client, sourceMetadataConfig{
		URL:         strings.TrimRight(cfg.OutwayURL, "/") + cfg.SourceMetadataOutwayPath,
		ExpectedOIN: cfg.SourceMetadataOIN,
		PublicJWK:   publicJWK,
		TypeID:      cfg.SourceMetadataTypeID,
	})
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

	var document sourceMetadataDocument
	if err := json.Unmarshal(payload, &document); err != nil {
		return nil, fmt.Errorf("parse source metadata payload: %w", err)
	}
	if document.SourceOIN != cfg.ExpectedOIN {
		return nil, fmt.Errorf("source metadata OIN %q does not match registered OIN %q", document.SourceOIN, cfg.ExpectedOIN)
	}
	for _, definition := range document.Attestations {
		if definition.TypeID != cfg.TypeID {
			continue
		}
		if err := validateSourceAttestation(definition); err != nil {
			return nil, fmt.Errorf("attestation %q: %w", cfg.TypeID, err)
		}
		return &sourceMetadataShadow{Version: document.Version, Definition: definition}, nil
	}
	return nil, fmt.Errorf("source metadata has no attestation %q", cfg.TypeID)
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
	for claim, rule := range definition.Mapping {
		if claim == "" || rule.Pointer == "" || !strings.HasPrefix(rule.Pointer, "/") {
			return fmt.Errorf("mapping %q must contain an absolute JSON pointer", claim)
		}
	}
	return nil
}

func (s *sourceMetadataShadow) appliesTo(uc Usecase) bool {
	return s != nil && uc.bron() == bronBD && len(uc.Belastingjaren) == 1
}

func (s *sourceMetadataShadow) queryPlan(bsn string, uc Usecase) (sourceQueryPlan, error) {
	if !s.appliesTo(uc) {
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
	var root any
	if err := json.Unmarshal(rawGraphQL, &root); err != nil {
		return false, fmt.Errorf("decode GraphQL response for shadow projection: %w", err)
	}
	selected, ok := jsonPointer(root, s.Definition.GraphQL.ResultPointer)
	if !ok {
		return false, fmt.Errorf("result_pointer %q does not exist", s.Definition.GraphQL.ResultPointer)
	}
	rows, ok := selected.([]any)
	if !ok || len(rows) != 1 {
		return false, fmt.Errorf("cardinality exactly_one expected one result")
	}
	for claim, rule := range s.Definition.Mapping {
		value, ok := jsonPointer(rows[0], rule.Pointer)
		if !ok {
			return false, fmt.Errorf("mapping pointer %q for claim %q does not exist", rule.Pointer, claim)
		}
		converted, err := convertMappedValue(value, rule.Datatype)
		if err != nil {
			return false, fmt.Errorf("map claim %q: %w", claim, err)
		}
		if !reflect.DeepEqual(converted, docs[0].Attributes[claim]) {
			return false, nil
		}
	}
	return true, nil
}

func jsonPointer(root any, pointer string) (any, bool) {
	if pointer == "" {
		return root, true
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	current := root
	for _, rawToken := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(rawToken, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[token]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
}

func convertMappedValue(value any, datatype string) (any, error) {
	switch datatype {
	case "integer":
		number, ok := value.(float64)
		if !ok || number != float64(int64(number)) {
			return nil, fmt.Errorf("value is not an integer")
		}
		return int64(number), nil
	case "gYear":
		number, ok := value.(float64)
		if !ok || number != float64(int64(number)) {
			return nil, fmt.Errorf("value is not a year")
		}
		return number, nil
	case "string", "date":
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("value is not a string")
		}
		return text, nil
	default:
		return nil, fmt.Errorf("unsupported datatype %q", datatype)
	}
}
