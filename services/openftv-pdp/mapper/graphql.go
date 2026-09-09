package mapping

// GraphQLToContext implements the GBO context-handler as an OpenFTV
// request-mapper. It walks the GraphQL query carried in the action's
// body attribute and enriches the PARC context with everything the
// authz policy needs:
//
//   - context.resolved  — {fields, args, coverage_unverifiable} from the
//     query walk. Schema-less: scalar = no selection set, parent types
//     come from the generated field-map (GBO_FIELD_MAP).
//   - context.resource  — {scope, query, variables}.
//   - context.trace_id  — Fsc-Transaction-Id (falls back to X-Request-Id).
//   - context.fsc       — {transaction_id}.
//   - context.pip       — {consent} for the DvTP flow, {pid: {pi}} for
//     the EUDI flow. Never the BSN; see pseudonymizeBSN.
//
// Flow dispatch reads the trusted grant property in the FSC token and
// nothing else. A request that carries no flow gets an empty one, which
// matches no rule (engine.rego `_flow_applicable`) and therefore denies
// with NO_APPLICABLE_RULE.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/models"
)

const (
	gqlMaxDepth         = 64
	consentClockSkew    = 30 * time.Second
	consentJWKSCacheTTL = 5 * time.Minute
	consentJWKSMaxStale = time.Hour
)

// GraphQLToContext detects a GraphQL body in the action attributes and
// enriches the context. Bodies that do not decode as a GraphQL request
// mark the coverage unverifiable (fail-closed) instead of erroring.
func GraphQLToContext(parc *models.PARC, opts ...Option) *models.PARC {
	headers := headerMap(parc.Context.GetAttributeValue(models.AttrHeaders))
	flow := flowFromHeaders(headers)
	txID := firstHeader(headers, "Fsc-Transaction-Id", "X-Request-Id", "X-Request-ID")

	ctx := models.NewAttributeSet(parc.Context)
	ctx.AddAttributeKV("trace_id", txID)
	ctx.AddAttributeKV("flow", flow)
	ctx.AddAttributeKV("fsc", map[string]any{"transaction_id": txID})

	attr := parc.Action.Attributes().GetAttribute(models.AttrBody)
	if attr == nil {
		return &models.PARC{Principal: parc.Principal, Action: parc.Action, Resource: parc.Resource, Context: ctx}
	}
	body, ok := attr.Value().(string)
	if !ok || body == "" {
		return &models.PARC{Principal: parc.Principal, Action: parc.Action, Resource: parc.Resource, Context: ctx}
	}

	query, variables := decodeGraphQLBody(body)
	scope := firstHeader(headers, "X-Gbo-Scope", "X-GBO-Scope")

	resource := map[string]any{
		"scope":     scope,
		"query":     query,
		"variables": variables,
	}

	ctx.AddAttributeKV("resource", resource)
	ctx.AddAttributeKV("resolved", walkQuery(query, variables))

	if isEUDIFlow(flow) {
		// The BSN stops here. It is needed to reach the bron — the PEP
		// forwards the original query untouched — but the policy engine
		// evaluates on a pseudonymous identity, so that is all it is
		// given. On pseudonymize failure the BSN is scrubbed to "" (fail
		// closed: PID_NOT_PRESENT), never passed through.
		bsn, _ := variables["bsn"].(string)
		pi, err := pseudonymizeBSN(bsn)
		if err != nil {
			pi = ""
		}
		ctx.AddAttributeKV("pip", map[string]any{"pid": map[string]any{"pi": pi}})
		action := parc.Action
		if bsn != "" {
			substituteContext(ctx, bsn, pi)
			// The raw body attribute is part of the decision-log input —
			// rewrite the identifier in it too.
			variables["bsn"] = pi
			newBody, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
			action = models.NewEntity(parc.Action.Type(), parc.Action.ID(), models.NewAttributeSet(parc.Action.Attributes()))
			action.Attributes().AddAttributeKV(models.AttrBody, string(newBody))
		}
		return &models.PARC{Principal: parc.Principal, Action: action, Resource: parc.Resource, Context: ctx}
	} else if flow == "dvtp:query" {
		ctx.AddAttributeKV("pip", map[string]any{"consent": fetchConsent(headers)})
	} else {
		ctx.AddAttributeKV("pip", map[string]any{"consent": invalidConsent("flow is not dvtp:query")})
	}

	return &models.PARC{
		Principal: parc.Principal,
		Action:    parc.Action,
		Resource:  parc.Resource,
		Context:   ctx,
	}
}

// pseudonymizeBSN resolves the wallet-disclosed BSN to a PI via BSNk,
// so the policy engine (and its decision log, shipped to Loki) never
// holds the BSN itself. No rule reads the identifier's value: the EUDI
// rules only assert that a PID was disclosed, and the DvTP rule's
// constraint-binding compares PI against PI.
func pseudonymizeBSN(bsn string) (string, error) {
	if bsn == "" {
		return "", nil
	}
	u := bsnkURL() + "/pseudonymize"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(`{"bsn":"`+bsn+`"}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := consentClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bsnk status %d", resp.StatusCode)
	}
	var out struct {
		PI string `json:"pi"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.PI == "" {
		return "", fmt.Errorf("bsnk returned no PI")
	}
	return out.PI, nil
}

func bsnkURL() string {
	if u := os.Getenv("GBO_BSNK_URL"); u != "" {
		return u
	}
	return "http://bsnk-mock:4003"
}

// substituteContext rewrites every occurrence of `from` to `to` in the
// context attributes the mapper just set, at JSON value level rather
// than by string search, so a BSN that happens to be a substring of
// some other value is left alone. Covers resource.variables and the
// resolved args (both derived from the query variables).
func substituteContext(ctx *models.AttributeSet, from, to string) {
	for _, key := range []string{"resource", "resolved"} {
		attr := ctx.GetAttribute(key)
		if attr == nil {
			continue
		}
		ctx.AddAttributeKV(key, substituteValue(attr.Value(), from, to))
	}
}

// isEUDIFlow recognises the wallet attestation flow. Bron-specific
// variants (`eudi:attestation:brp`) are gone: the bronprofiel is not an
// axis of the flow, and both EUDI rules dispatch on the base name. A
// suffixed value is therefore not a wallet flow — engine_test.rego
// asserts it matches no rule, so accepting it here would pseudonymise a
// request the policy is about to deny anyway.
func isEUDIFlow(flow string) bool {
	return flow == "eudi:attestation"
}

func substituteValue(v any, from, to string) any {
	switch t := v.(type) {
	case string:
		if t == from {
			return to
		}
		return t
	case map[string]any:
		for k, val := range t {
			t[k] = substituteValue(val, from, to)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = substituteValue(val, from, to)
		}
		return t
	default:
		return v
	}
}

// ── Consent PIP (per-request, fail-closed) ─────────────────────────────────

type consentClaims struct {
	ConsentID        string   `json:"consent_id"`
	PI               string   `json:"pi"`
	Scopes           []string `json:"scopes"`
	DienstverlenrOIN string   `json:"dienstverlener_oin"`
	ValidUntil       string   `json:"valid_until"`
	jwt.RegisteredClaims
}

type jwkSet struct {
	Keys []struct {
		KTY string `json:"kty"`
		CRV string `json:"crv"`
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		X   string `json:"x"`
		Y   string `json:"y"`
	} `json:"keys"`
}

func invalidConsent(reason string) map[string]any {
	return map[string]any{
		"context_valid":    false,
		"status_available": false,
		"exists":           false,
		"invalid_reason":   reason,
	}
}

// fetchConsent verifies the complete signed authorization context first and
// then checks only the referenced consent's online status. No claim is filled
// from a different request or an alternate consent record.
func fetchConsent(headers map[string]string) map[string]any {
	tokenString := headers["x-gbo-consent-token"]
	if tokenString == "" {
		return invalidConsent("consent token missing")
	}
	claims := &consentClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodES256 {
			return nil, fmt.Errorf("unexpected consent signing algorithm")
		}
		if typ, _ := token.Header["typ"].(string); typ != "gbo-consent+jwt" {
			return nil, fmt.Errorf("unexpected consent token type")
		}
		kid, _ := token.Header["kid"].(string)
		if kid == "" {
			return nil, fmt.Errorf("consent signing key id missing")
		}
		return consentSigningKey(kid)
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}),
		jwt.WithIssuer(consentIssuer()),
		jwt.WithAudience(consentAudience()),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(consentClockSkew),
	)
	if err != nil || token == nil || !token.Valid {
		return invalidConsent("consent token verification failed")
	}
	if claims.ConsentID == "" || claims.PI == "" || claims.Scopes == nil || claims.DienstverlenrOIN == "" ||
		claims.ValidUntil == "" || claims.ID == "" || claims.IssuedAt == nil || claims.NotBefore == nil || claims.ExpiresAt == nil {
		return invalidConsent("required consent claims missing")
	}
	validUntil := claims.ValidUntil
	parsedValidUntil, validUntilErr := time.Parse(time.RFC3339, validUntil)
	if validUntilErr != nil || claims.ExpiresAt == nil || !parsedValidUntil.Equal(claims.ExpiresAt.Time) {
		return invalidConsent("valid_until does not match exp")
	}
	status, found, err := fetchConsentStatus(claims.ConsentID, firstHeader(headers, "Fsc-Transaction-Id", "X-Request-Id", "X-Request-ID"))
	if err != nil {
		return map[string]any{
			"context_valid":    true,
			"status_available": false,
			"exists":           false,
			"consent_id":       claims.ConsentID,
		}
	}
	if found && status != "ACTIVE" && status != "REVOKED" {
		return map[string]any{
			"context_valid":    true,
			"status_available": false,
			"exists":           false,
			"consent_id":       claims.ConsentID,
		}
	}
	return map[string]any{
		"context_valid":      true,
		"status_available":   true,
		"exists":             found,
		"withdrawn":          found && status == "REVOKED",
		"granted_scopes":     claims.Scopes,
		"valid_until":        validUntil,
		"pi":                 claims.PI,
		"dienstverlener_oin": claims.DienstverlenrOIN,
		"consent_id":         claims.ConsentID,
		"jti":                claims.ID,
	}
}

type consentKeyCache struct {
	mu         sync.Mutex
	source     string
	keys       map[string]*ecdsa.PublicKey
	freshUntil time.Time
	staleUntil time.Time
	now        func() time.Time
}

var cachedConsentKeys = consentKeyCache{now: time.Now}

// consentSigningKey keeps known verification keys local to the PDP. An
// unknown kid always triggers a refresh so key rotation takes effect without
// waiting for the TTL. During a brief JWKS outage a previously verified key
// remains usable for a bounded period; the separate online consent-status
// check still fails closed if the consent register itself is unavailable.
func consentSigningKey(kid string) (*ecdsa.PublicKey, error) {
	cache := &cachedConsentKeys
	cache.mu.Lock()
	defer cache.mu.Unlock()

	source := consentURL()
	now := cache.now()
	if cache.source != source {
		cache.source = source
		cache.keys = nil
		cache.freshUntil = time.Time{}
		cache.staleUntil = time.Time{}
	}

	known := cache.keys[kid]
	if known != nil && now.Before(cache.freshUntil) {
		return known, nil
	}

	keys, err := fetchConsentKeys(source)
	if err != nil {
		if known != nil && now.Before(cache.staleUntil) {
			return known, nil
		}
		return nil, fmt.Errorf("consent verification keys unavailable: %w", err)
	}

	cache.keys = keys
	cache.freshUntil = now.Add(consentJWKSCacheTTL)
	cache.staleUntil = now.Add(consentJWKSMaxStale)
	key := keys[kid]
	if key == nil {
		return nil, fmt.Errorf("unknown consent signing key")
	}
	return key, nil
}

func fetchConsentKeys(source string) (map[string]*ecdsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source+"/.well-known/jwks.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := consentClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks status %d", resp.StatusCode)
	}
	var set jwkSet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, err
	}
	keys := make(map[string]*ecdsa.PublicKey, len(set.Keys))
	for _, key := range set.Keys {
		if key.KTY != "EC" || key.CRV != "P-256" || key.Alg != "ES256" || key.Kid == "" {
			continue
		}
		x, errX := base64.RawURLEncoding.DecodeString(key.X)
		y, errY := base64.RawURLEncoding.DecodeString(key.Y)
		if errX != nil || errY != nil {
			continue
		}
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if pub.Curve.IsOnCurve(pub.X, pub.Y) {
			keys[key.Kid] = pub
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("jwks contains no supported keys")
	}
	return keys, nil
}

// fetchConsentStatus asks the consent register whether the consent is still
// live. txID is passed on rather than dropped: confirming a status is itself
// a Dataverwerking that the register logs, and without the identifier that
// record lands under a trace of its own — outside the very request it was
// part of, and therefore invisible in a chain view that joins on it.
func fetchConsentStatus(consentID, txID string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	u := consentURL() + "/consents/" + url.PathEscape(consentID) + "/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", false, err
	}
	if txID != "" {
		req.Header.Set("Fsc-Transaction-Id", txID)
	}
	resp, err := consentClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("consent status %d", resp.StatusCode)
	}
	var status struct {
		ConsentID string `json:"consent_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return "", false, err
	}
	if status.ConsentID != consentID || status.Status == "" {
		return "", false, fmt.Errorf("invalid consent status response")
	}
	return status.Status, true, nil
}

var consentClient = &http.Client{Timeout: 2 * time.Second}

func consentURL() string {
	if u := os.Getenv("GBO_CONSENT_URL"); u != "" {
		return u
	}
	return "http://consent-register:4002"
}

func consentIssuer() string {
	if value := os.Getenv("GBO_CONSENT_ISSUER"); value != "" {
		return value
	}
	return "https://consent-register.gbo.test"
}

func consentAudience() string {
	if value := os.Getenv("GBO_CONSENT_AUDIENCE"); value != "" {
		return value
	}
	return "gbo:dvtp:pdp"
}

// decodeGraphQLBody accepts the body as a plain JSON string (FSC-Inway
// stringifies JSON bodies) or base64-encoded.
func decodeGraphQLBody(body string) (string, map[string]any) {
	var inner struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(body), &inner); err != nil {
		if d, err2 := base64.StdEncoding.DecodeString(body); err2 == nil {
			_ = json.Unmarshal(d, &inner)
		}
	}
	if inner.Variables == nil {
		inner.Variables = map[string]any{}
	}
	return inner.Query, inner.Variables
}

// headerMap normalizes the context headers attribute into a
// case-insensitive lookup map (values are joined single strings).
func headerMap(v any) map[string]string {
	out := map[string]string{}
	switch h := v.(type) {
	case map[string]string:
		for k, val := range h {
			out[strings.ToLower(k)] = val
		}
	case map[string]any:
		for k, val := range h {
			if s, ok := val.(string); ok {
				out[strings.ToLower(k)] = s
			}
		}
	}
	return out
}

func firstHeader(headers map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := headers[strings.ToLower(k)]; v != "" {
			return v
		}
	}
	return ""
}

// flowFromHeaders dispatches on the trusted 'prp' claim in the FSC
// access-token, and on nothing else. The token is read unsafely:
// FSC-Inway validated the signature before invoking the PDP
// (chain-of-trust).
//
// The flow is a property of the FSC grant (fsc-core §Properties): it is
// agreed when the contract is negotiated, covered by both peers'
// signatures because it is part of the grant hash, and emitted by the
// provider's Manager as `prp` — so a caller has no say in which
// authorization regime it is judged under. There is deliberately no
// header fallback and no default: absent a claim the flow is empty,
// which matches no rule and denies. Defaulting to `dvtp:query` would
// silently select the consent-based regime for a request that never
// asked for it.
func flowFromHeaders(headers map[string]string) string {
	auth := headers["fsc-authorization"]
	if auth == "" {
		return ""
	}
	claims := tokenClaims(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer")))
	if claims == nil {
		return ""
	}
	props, ok := claims["prp"].(map[string]any)
	if !ok {
		return ""
	}
	flow, _ := props["flow"].(string)
	return flow
}

func tokenClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if payload, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return nil
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims
}

// ── Query walk ────────────────────────────────────────────────────────────

var (
	fieldMapOnce sync.Once
	fieldMapData map[string]string
)

// fieldMap lazily loads the generated field→parent-type map. A missing
// or unreadable file yields an empty map: every nested field then gets
// parent "?", which the closed-world policy denies (fail-closed).
func fieldMap() map[string]string {
	fieldMapOnce.Do(func() {
		fieldMapData = map[string]string{}
		path := os.Getenv("GBO_FIELD_MAP")
		if path == "" {
			path = "/etc/gbo/field-map.json"
		}
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &fieldMapData)
		}
	})
	return fieldMapData
}

type gqlWalkCtx struct {
	fields               []map[string]any
	args                 map[string]any
	variables            map[string]any
	fragments            map[string]*ast.FragmentDefinition
	fragSeen             map[string]bool
	coverageUnverifiable bool
}

// walkQuery parses the query and produces the pre-digested resolved-
// shape the authz policy consumes. Never touches coverage config —
// `scalar` is derived from the selection set (no SDL at runtime); the
// parent type comes from the generated field-map.
func walkQuery(query string, variables map[string]any) map[string]any {
	res := map[string]any{"fields": []map[string]any{}, "args": map[string]any{}, "coverage_unverifiable": false}

	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil || doc == nil {
		res["coverage_unverifiable"] = true
		return res
	}
	var op *ast.OperationDefinition
	for _, o := range doc.Operations {
		op = o
		break
	}
	if op == nil {
		res["coverage_unverifiable"] = true
		return res
	}

	ctx := &gqlWalkCtx{
		fields:    []map[string]any{},
		args:      map[string]any{},
		variables: variables,
		fragments: map[string]*ast.FragmentDefinition{},
		fragSeen:  map[string]bool{},
	}
	for _, f := range doc.Fragments {
		ctx.fragments[f.Name] = f
	}

	walkSelections(op.SelectionSet, "Query", []string{}, ctx)

	for k, v := range variables {
		if s, ok := v.(string); !ok || s != "" {
			ctx.args["vars."+k] = v
		}
	}

	res["fields"] = ctx.fields
	res["args"] = ctx.args
	res["coverage_unverifiable"] = ctx.coverageUnverifiable
	return res
}

func walkSelections(sels ast.SelectionSet, parentType string, pathSegs []string, ctx *gqlWalkCtx) {
	if len(pathSegs) > gqlMaxDepth {
		ctx.coverageUnverifiable = true
		return
	}
	for _, sel := range sels {
		switch s := sel.(type) {
		case *ast.Field:
			name := s.Name
			segs := append(append([]string(nil), pathSegs...), name)
			childType := fieldMap()[parentType+"."+name]
			ctx.fields = append(ctx.fields, map[string]any{
				"parent": parentType,
				"name":   name,
				"scalar": s.SelectionSet == nil,
				"known":  true,
				"id":     "Query." + strings.Join(segs, "."),
			})
			for _, arg := range s.Arguments {
				flattenValue([]string{arg.Name}, arg.Value, ctx)
			}
			if s.SelectionSet != nil {
				if childType == "" {
					childType = "?"
				}
				walkSelections(s.SelectionSet, childType, segs, ctx)
			}
		case *ast.FragmentSpread:
			frag, ok := ctx.fragments[s.Name]
			if !ok || ctx.fragSeen[s.Name] {
				ctx.coverageUnverifiable = true
				continue
			}
			ctx.fragSeen[s.Name] = true
			cond := frag.TypeCondition
			if cond == "" {
				cond = parentType
			}
			walkSelections(frag.SelectionSet, cond, pathSegs, ctx)
			delete(ctx.fragSeen, s.Name)
		case *ast.InlineFragment:
			cond := parentType
			if s.TypeCondition != "" {
				cond = s.TypeCondition
			}
			walkSelections(s.SelectionSet, cond, pathSegs, ctx)
		}
	}
}

// flattenValue flattens a GraphQL value node into the "<arg>.<path>"
// key convention. Variables resolve from the request variables;
// null/empty variables are dropped ("not supplied").
func flattenValue(prefix []string, v *ast.Value, ctx *gqlWalkCtx) {
	if v == nil {
		return
	}
	key := strings.Join(prefix, ".")
	switch v.Kind {
	case ast.ObjectValue:
		for _, child := range v.Children {
			flattenValue(append(append([]string(nil), prefix...), child.Name), child.Value, ctx)
		}
	case ast.ListValue:
		for i, child := range v.Children {
			flattenValue(append(append([]string(nil), prefix...), strconv.Itoa(i)), child.Value, ctx)
		}
	case ast.Variable:
		if vv, ok := ctx.variables[v.Raw]; ok {
			if s, isStr := vv.(string); !isStr || s != "" {
				ctx.args[key] = vv
			}
		}
	case ast.NullValue:
		ctx.args[key] = nil
	default:
		ctx.args[key] = v.Raw
	}
}
