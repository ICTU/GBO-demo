package mapping

// GraphQLToContext implements the GBO context-handler as an OpenFTV
// request-mapper. It walks the GraphQL query carried in the action's
// body attribute and enriches the PARC context with what the authz
// policy needs from the request itself:
//
//   - context.resolved  — {fields, args, coverage_unverifiable} from the
//     query walk. Schema-less: scalar = no selection set, parent types
//     come from the generated field-map (GBO_FIELD_MAP).
//   - context.resource  — {scope, query, variables}.
//   - context.trace_id  — Fsc-Transaction-Id (falls back to X-Request-Id).
//   - context.fsc       — {transaction_id}.
//   - context.pip       — {pid: {pi}} when the request carries no consent
//     token. Never the BSN; see pseudonymizeBSN.
//
// The consent is not resolved here. The policy verifies a consent token
// and reads the consent's live status itself (policies/dvtp/gbo/
// consent.rego, #330): attribute retrieval is PDP work under FTV, and a
// revocation has to deny on the first request after it.
//
// The authorization regime is derived from the evidence on the request,
// not from a grant property: a consent token selects the consent regime,
// its absence the PID regime. Neither is trusted on its face — the policy
// verifies the token, and every rule re-checks its own basis and fails
// closed. A request carrying both is denied by the engine
// (AMBIGUOUS_EVIDENCE) rather than resolved by precedence.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
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
	headers := headerMap(parc.Context.GetAttributeValue(models.AttrHeaders))
	txID := firstHeader(headers, "Fsc-Transaction-Id", "X-Request-Id", "X-Request-ID")

	ctx := models.NewAttributeSet(parc.Context)
	ctx.AddAttributeKV("trace_id", txID)
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

	subject, _ := variables["bsn"].(string)

	// The regime follows from the evidence the request carries, not from a
	// property somebody declared (#334). Only one signal can be read here,
	// before any rule runs: a consent token. Its presence selects the
	// consent regime; the policy verifies it — signature, issuer, audience,
	// expiry — and denies when it does not hold.
	//
	// Everything else falls through to the PID regime. That asymmetry is
	// deliberate but not free: a subject variable cannot discriminate,
	// because BOTH regimes carry one (DvTP sends a PI under
	// subject_id_type=pseudonym, EUDI a raw BSN), and telling them apart by
	// identifier shape is the guessing this change exists to remove. So the
	// PID regime is entered by ABSENCE of consent, and the only gate left on
	// it is each EUDI rule's own allowed_actors whitelist. That whitelist
	// must stay disjoint from the consent-based consumers — policies/dvtp/
	// gbo/engine_test.rego asserts it, because an OIN in both could skip
	// consent simply by omitting this header.
	if hasConsentEvidence(headers) {
		// Scrubbing does not depend on which regime the request claims: the
		// header decides HOW the subject identifier is made safe, never
		// WHETHER. The consent regime expects a pseudonym — the consent's own
		// PI, which DVT0001's constraint binding compares against the verified
		// token. That comparison is the policy's now; this mapper does not
		// verify the token, so it keeps a subject only if it has the shape of
		// a PI and blanks anything else. Nothing of that shape is a BSN, so no
		// BSN reaches the decision input, whether the token verifies or not.
		// Shape decides only what may stay in the input here — never the
		// regime, which is what the paragraph above rules out.
		//
		// It never pseudonymises: that would turn a BSN into that citizen's
		// PI, which would then satisfy the binding for a consumer that was
		// never meant to hold the BSN. Blanked, the binding fails with
		// CONSTRAINT_MISMATCH, as it does for a PI other than the consent's.
		if isPseudonym(subject) {
			return &models.PARC{Principal: parc.Principal, Action: parc.Action, Resource: parc.Resource, Context: ctx}
		}
		return replaceSubject(parc, ctx, query, variables, subject, "")
	}

	// PID regime. The BSN stops here. It is needed to reach the bron — the
	// PEP forwards the original query untouched — but the policy engine
	// evaluates on a pseudonymous identity, so that is all it is given. On
	// pseudonymize failure the BSN is scrubbed to "" (fail closed:
	// PID_NOT_PRESENT), never passed through. pip.pid is set even then: it
	// records that PID enrichment was attempted, which is what keeps the
	// deny reason in this regime. A request with no subject variable yields
	// an empty pi and denies the same way.
	pi, err := pseudonymizeBSN(subject)
	if err != nil {
		pi = ""
	}
	ctx.AddAttributeKV("pip", map[string]any{"pid": map[string]any{"pi": pi}})
	return replaceSubject(parc, ctx, query, variables, subject, pi)
}

// replaceSubject rewrites the subject identifier everywhere the decision
// sees it: the context attributes the mapper just set, and the raw body
// attribute, which is part of the decision-log input too. An empty subject
// leaves nothing to replace.
func replaceSubject(parc *models.PARC, ctx *models.AttributeSet, query string, variables map[string]any, from, to string) *models.PARC {
	if from == "" || from == to {
		return &models.PARC{Principal: parc.Principal, Action: parc.Action, Resource: parc.Resource, Context: ctx}
	}
	substituteContext(ctx, from, to)
	variables["bsn"] = to
	newBody, _ := json.Marshal(map[string]any{"query": query, "variables": variables})
	action := models.NewEntity(parc.Action.Type(), parc.Action.ID(), models.NewAttributeSet(parc.Action.Attributes()))
	action.Attributes().AddAttributeKV(models.AttrBody, string(newBody))
	return &models.PARC{Principal: parc.Principal, Action: action, Resource: parc.Resource, Context: ctx}
}

// hasConsentEvidence reports whether the request carries a consent token,
// which is the one signal available before any rule runs that positively
// identifies the consent regime. It says nothing about whether the token is
// valid — the policy verifies that and fails closed — only that the caller
// is asking to be judged under consent rather than under a disclosed PID.
//
// An empty header value is not evidence: it would select the consent regime
// and then fail verification, denying with a consent reason a PID-based
// caller cannot act on.
func hasConsentEvidence(headers map[string]string) bool {
	return headers["x-gbo-consent-token"] != ""
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
	resp, err := bsnkClient.Do(req)
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

var bsnkClient = &http.Client{Timeout: 2 * time.Second}

// pseudonymPattern is the shape of a PI as BSNk issues it (services/
// bsnk-mock), and the shape lib.rego's PID check accepts. A BSN is nine
// digits, so nothing matching this can be one.
var pseudonymPattern = regexp.MustCompile(`^PI-[0-9a-f]{16}$`)

func isPseudonym(subject string) bool {
	return pseudonymPattern.MatchString(subject)
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
