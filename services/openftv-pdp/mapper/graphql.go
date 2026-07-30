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
//   - context.pid       — {bsn} for the EUDI flow (from variables.bsn).
//
// Flow dispatch (dvtp:query vs eudi:attestation) follows the trusted
// additional-claim in the FSC token, then the X-GBO-Flow header.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

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

	query, variables := decodeGraphQLBody(body)
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
	ctx.AddAttributeKV("resolved", walkQuery(query, variables))
	ctx.AddAttributeKV("trace_id", txID)
	ctx.AddAttributeKV("flow", flow)
	ctx.AddAttributeKV("fsc", map[string]any{"transaction_id": txID})

	if flow == "eudi:attestation" {
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
	} else {
		ctx.AddAttributeKV("pip", map[string]any{"consent": fetchConsent(headers, variables, scope)})
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

// fetchConsent retrieves the ACTIVE consent for (PI, scope) from the
// consent-register. Per-request so a revoke is effective immediately
// (the PIP pull-config alternative was both stale and broken upstream —
// the in-memory attribute store's CAS rejects complex valueAsIs values).
// Fail-soft: any error yields exists=false → CONSENT_NOT_FOUND.
func fetchConsent(headers map[string]string, variables map[string]any, scope string) map[string]any {
	notFound := map[string]any{"exists": false}

	pi, _ := variables["bsn"].(string)
	if pi == "" {
		return notFound
	}
	u := consentURL() + "/consents?pi=" + url.QueryEscape(pi) + "&scope=" + url.QueryEscape(scope)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return notFound
	}
	resp, err := consentClient.Do(req)
	if err != nil {
		return notFound
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return notFound
	}
	var list []struct {
		Status     string   `json:"status"`
		Scopes     []string `json:"scopes"`
		ValidUntil string   `json:"valid_until"`
		PI         string   `json:"pi"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil || len(list) == 0 {
		return notFound
	}
	// Prefer an ACTIVE consent; otherwise report the (revoked) record so
	// the policy can deny with CONSENT_WITHDRAWN instead of NOT_FOUND.
	c := list[0]
	for _, item := range list {
		if item.Status == "ACTIVE" {
			c = item
			break
		}
	}
	return map[string]any{
		"exists":         true,
		"withdrawn":      c.Status == "REVOKED",
		"granted_scopes": c.Scopes,
		"valid_until":    c.ValidUntil,
		"pi":             c.PI,
	}
}

var consentClient = &http.Client{Timeout: 2 * time.Second}

func consentURL() string {
	if u := os.Getenv("GBO_CONSENT_URL"); u != "" {
		return u
	}
	return "http://consent-register:4002"
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

// flowFromHeaders dispatches on the trusted additional-claim ('add',
// legacy 'prp') in the FSC access-token, then the untrusted X-GBO-Flow
// header. The token is read unsafely: FSC-Inway validated the signature
// before invoking the PDP (chain-of-trust).
func flowFromHeaders(headers map[string]string) string {
	if auth := headers["fsc-authorization"]; auth != "" {
		if claims := tokenClaims(strings.TrimSpace(strings.TrimPrefix(auth, "Bearer"))); claims != nil {
			for _, key := range []string{"add", "prp"} {
				if props, ok := claims[key].(map[string]any); ok {
					if f, ok := props["flow"].(string); ok && f != "" {
						return f
					}
				}
			}
		}
	}
	if f := firstHeader(headers, "X-Gbo-Flow", "X-GBO-Flow"); f != "" {
		return f
	}
	return "dvtp:query"
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
