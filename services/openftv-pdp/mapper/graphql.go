package mapping

// GraphQLToContext implements the GBO context-handler as an OpenFTV
// request-mapper. It walks the GraphQL query carried in the action's
// body attribute and adds what the authz policy needs from it:
//
//   - context.resolved  — {fields, args, coverage_unverifiable} from the
//     query walk. Schema-less: scalar = no selection set, parent types
//     come from the generated field-map (GBO_FIELD_MAP).
//   - context.resource  — {scope, query, variables}.
//   - context.trace_id  — Fsc-Transaction-Id (falls back to X-Request-Id).
//   - context.fsc       — {transaction_id}.
//
// That is all it does. It resolves no attribute: the policy verifies a
// consent token and reads the consent's live status itself (policies/dvtp/
// gbo/consent.rego, #330), and reads the PID regime from the request
// (#364). The request, the subject identifier included, reaches the policy
// as it was sent; keeping a plain BSN out of the decision logs is #368.

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"

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

	// No pip: the authorization regime follows from the evidence on the
	// request (#334), and the policy reads that evidence itself — a consent
	// token from the headers, the subject from the query (#330, #364).
	return &models.PARC{Principal: parc.Principal, Action: parc.Action, Resource: parc.Resource, Context: ctx}
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
