package mapping

// GraphQLToContext implements the GBO context-handler as an OpenFTV
// request-mapper. It walks the GraphQL query carried in the action's
// body attribute and enriches the PARC context with everything the
// authz policy needs:
//
//   - context.resolved  — {fields, args, coverage_unverifiable} from the
//     query walk. `scalar` comes from the selection set; parent types come
//     from the SDLs shipped with the policies (GBO_SCHEMA_DIR).
//   - context.resource  — {scope, query, variables}.
//   - context.trace_id  — Fsc-Transaction-Id (falls back to X-Request-Id).
//   - context.fsc       — {transaction_id}.
//   - context.flow      — from the signed FSC additional-claim only.
//   - context.pip.pid   — {pi} for the EUDI flow, pseudonymised via BSNk.
//
// Consent is NOT fetched here — it is PIP work and the policy retrieves it
// during evaluation. See policies/dvtp/gbo/consent.rego.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"gitlab.com/digilab.overheid.nl/ecosystem/ftv/open-ftv/eam/models"
)

const gqlMaxDepth = 64

// GraphQLToContext detects a GraphQL body in the action attributes and
// enriches the context. Bodies that do not decode as a GraphQL request
// mark the coverage unverifiable (fail-closed) instead of erroring.
func GraphQLToContext(parc *models.PARC, opts ...Option) *models.PARC {
	attr := parc.Action.Attributes().GetAttribute(models.AttrBody)
	if attr == nil {
		return parc
	}
	body, ok := attr.Value().(string)
	if !ok || body == "" {
		return parc
	}

	query, operationName, variables := decodeGraphQLBody(body)
	headers := headerMap(parc.Context.GetAttributeValue(models.AttrHeaders))
	flow := flowFromHeaders(headers)
	scope := firstHeader(headers, "X-Gbo-Scope", "X-GBO-Scope")
	txID := firstHeader(headers, "Fsc-Transaction-Id", "X-Request-Id", "X-Request-ID")

	resource := map[string]any{
		"scope":     scope,
		"query":     query,
		"variables": variables,
	}

	ctx := models.NewAttributeSet(parc.Context)
	ctx.AddAttributeKV("resource", resource)
	ctx.AddAttributeKV("resolved", walkQuery(query, operationName, variables))
	ctx.AddAttributeKV("trace_id", txID)
	ctx.AddAttributeKV("flow", flow)
	ctx.AddAttributeKV("fsc", map[string]any{"transaction_id": txID})

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
	}

	// DvTP carries no PID. Consent is not fetched here: it is PIP work,
	// and the standard puts attribute retrieval in the PDP during
	// evaluation, so the policy asks the consent-register itself. Doing it
	// here also meant a mapper reshaping a request it had already been
	// given, using our register's URL shape and response model — the
	// largest GBO-specific block in an otherwise generic mapper.
	return &models.PARC{
		Principal: parc.Principal,
		Action:    parc.Action,
		Resource:  parc.Resource,
		Context:   ctx,
	}
}

// isEUDIFlow reports whether the flow disclosed a PID through the wallet
// and therefore carries a BSN that must not reach the policy engine.
//
// Prefix, not equality: the flow string also carries the bronprofiel
// ("eudi:attestation:brp"), so an exact match on "eudi:attestation" sent
// the BRP flow down the consent branch — no pseudonymisation, the BSN
// into the consent-register query and into the decision log. Matching
// the whole "eudi:" family keeps a future flow fail-safe by default.
func isEUDIFlow(flow string) bool {
	return strings.HasPrefix(flow, "eudi:")
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
	resp, err := httpClient.Do(req)
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

var httpClient = &http.Client{Timeout: 2 * time.Second}

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

// decodeGraphQLBody accepts the body as a plain JSON string (FSC-Inway
// stringifies JSON bodies) or base64-encoded. operationName is read
// because the source executes the operation it names, not the first one.
func decodeGraphQLBody(body string) (query, operationName string, variables map[string]any) {
	var inner struct {
		Query         string         `json:"query"`
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(body), &inner); err != nil {
		if d, err2 := base64.StdEncoding.DecodeString(body); err2 == nil {
			_ = json.Unmarshal(d, &inner)
		}
	}
	if inner.Variables == nil {
		inner.Variables = map[string]any{}
	}
	return inner.Query, inner.OperationName, inner.Variables
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

// flowFromHeaders reads the flow from the additional-claim ('add', legacy
// 'prp') in the FSC access-token, and nowhere else.
//
// The flow is a property of the FSC grant: additional-claims-service
// matches on (outway_peer_id, service_peer_id, service_name) and the
// provider's FSC-Manager signs the result into the token. The same
// service yields dvtp:query for one consumer and eudi:attestation for
// another — decided at contract time, by the source-holder, signed.
//
// There is deliberately no header fallback and no default. A header let
// the caller name the regime it wanted to be judged under, and defaulting
// to dvtp:query silently selected consent-based rules for a request that
// had said nothing — fail-open in a security dispatch. An empty flow now
// matches no rule's dispatch, so the closed-world engine denies with
// NO_APPLICABLE_RULE.
//
// The token is read without verifying its signature: FSC-Inway validated
// it before invoking the PDP (chain-of-trust).
func flowFromHeaders(headers map[string]string) string {
	auth := headers["fsc-authorization"]
	if auth == "" {
		return ""
	}
	claims := tokenClaims(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer")))
	if claims == nil {
		return ""
	}
	for _, key := range []string{"add", "prp"} {
		if props, ok := claims[key].(map[string]any); ok {
			if f, ok := props["flow"].(string); ok && f != "" {
				return f
			}
		}
	}
	return ""
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

// fieldMap lazily builds the field→return-type map from the GraphQL SDLs
// that ship alongside the policies.
//
// The walk itself is schema-less, but coverage keys are type-qualified
// ("Huwelijk.partners"), and a type name is not present in a query
// document — only the schema knows that `partners` returns a
// NatuurlijkPersoon. Type-qualification is unavoidable here because the
// type graph is cyclic (NatuurlijkPersoon.heeftOuder → NatuurlijkPersoon),
// so a path-based key would need an infinite family of paths to say what
// one type-qualified key says.
//
// The SDL is read from the policy directory rather than baked in as a
// generated artifact: the schema is part of the catalogue, so it belongs
// with the policies the PAP manages, and it hot-reloads with them. It is
// deliberately not fetched by introspection — that would make the source
// the authority on its own type graph, and relabelling a field's parent
// type would silently widen the coverage a rule grants.
//
// Fail-closed: an unreadable directory or an invalid SDL yields an empty
// map, every nested field then gets parent "?", and the closed-world
// policy denies it.
func fieldMap() map[string]string {
	fieldMapOnce.Do(func() { fieldMapData = loadFieldMap(schemaDir()) })
	return fieldMapData
}

func schemaDir() string {
	if d := os.Getenv("GBO_SCHEMA_DIR"); d != "" {
		return d
	}
	return "/policies"
}

// loadFieldMap merges every *.graphql under dir into one field→type map.
// The bronprofielen are disjoint by design, and where they do share a type
// name the rules' covers_fields decide which fields are reachable, so a
// union is the right merge.
func loadFieldMap(dir string) map[string]string {
	m := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".graphql" {
			return nil //nolint:nilerr // an unreadable entry must not abort the rest
		}
		src, err := os.ReadFile(path) //nolint:gosec // operator-supplied policy dir
		if err != nil {
			return nil
		}
		schema, err := gqlparser.LoadSchema(&ast.Source{Name: path, Input: string(src)})
		if err != nil {
			return nil
		}
		for _, t := range schema.Types {
			if t.Kind != ast.Object && t.Kind != ast.Interface {
				continue
			}
			for _, f := range t.Fields {
				named := schema.Types[f.Type.Name()]
				if named != nil && (named.Kind == ast.Object || named.Kind == ast.Interface) {
					m[t.Name+"."+f.Name] = named.Name
				}
			}
		}
		return nil
	})
	return m
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
// `scalar` is derived from the selection set; the parent type comes from
// the SDL-derived field map.
func walkQuery(query, operationName string, variables map[string]any) map[string]any {
	res := map[string]any{"fields": []map[string]any{}, "args": map[string]any{}, "coverage_unverifiable": false}

	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil || doc == nil {
		res["coverage_unverifiable"] = true
		return res
	}
	op := selectOperation(doc, operationName)
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

// selectOperation picks the operation the source will actually execute.
//
// A document may hold several operations, and the executing server picks
// by operationName — so authorizing the first one would judge a different
// selection set than the one that runs. Anything ambiguous returns nil,
// which the caller turns into coverage_unverifiable (deny).
func selectOperation(doc *ast.QueryDocument, operationName string) *ast.OperationDefinition {
	if operationName != "" {
		for _, o := range doc.Operations {
			if o.Name == operationName {
				return o
			}
		}
		return nil // named but absent — do not fall back to another operation
	}
	if len(doc.Operations) != 1 {
		return nil // 0, or >1 without a name to disambiguate
	}
	return doc.Operations[0]
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
