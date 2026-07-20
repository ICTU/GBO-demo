// GBO context-handler — the GraphQL mechanism, in real code.
//
// Parses the raw GraphQL query, walks the selection set against the (clean)
// schema — expanding inline + spread fragments, recursing to any depth —
// and produces the pre-digested shape the OPA runtime will consume:
//
//	{ fields: [{parent, name, scalar, known, id}], args: {...}, coverage_unverifiable }
//
// Mechanism only: never looks at covers_types / covers_fields, rules, or
// roles. `scalar` is a schema fact (is this field a leaf or an object?);
// the inherit-or-deny policy lives entirely in the Rego runtime. Fail-closed:
// a parse error, an unknown fragment, or over-deep nesting → coverage_unverifiable=true.
//
// Port of iWlz reference resolve.js (Node/graphql-js → Go/vektah-gqlparser).
package main

import (
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

const maxDepth = 64 // generous DoS bound; real demo queries are far shallower

// resolvedField is the per-field descriptor consumed by the OPA
// runtime: parent type name, field name, scalar-vs-object
// classification, whether the schema knew the field (unknown fields
// are surfaced so the runtime denies them via NO_APPLICABLE_RULE),
// and the dotted id used in granted[]/denied_fields[].
type resolvedField struct {
	Parent string `json:"parent"`
	Name   string `json:"name"`
	Scalar bool   `json:"scalar"`
	Known  bool   `json:"known"`
	ID     string `json:"id"` // "Query.<path.to.leaf>"
}

type resolved struct {
	Fields               []resolvedField `json:"fields"`
	Args                 map[string]any  `json:"args"`
	CoverageUnverifiable bool            `json:"coverage_unverifiable"`
}

// buildResolved parses `query` against `schema` and returns the pre-digested
// shape the Rego runtime consumes. Variables are pre-substituted into the
// args map ("vars.<name>") so the runtime never re-resolves them.
func buildResolved(query string, variables map[string]any, schema *ast.Schema) resolved {
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil || doc == nil {
		return resolved{Fields: []resolvedField{}, Args: map[string]any{}, CoverageUnverifiable: true}
	}

	var op *ast.OperationDefinition
	for _, o := range doc.Operations {
		op = o
		break
	}
	if op == nil {
		return resolved{Fields: []resolvedField{}, Args: map[string]any{}, CoverageUnverifiable: true}
	}

	fragMap := make(map[string]*ast.FragmentDefinition, len(doc.Fragments))
	for _, f := range doc.Fragments {
		fragMap[f.Name] = f
	}

	ctx := &walkCtx{
		fields:   []resolvedField{},
		args:     map[string]any{},
		vars:     variables,
		fragMap:  fragMap,
		fragSeen: map[string]bool{},
		schema:   schema,
	}
	queryType := schema.Query
	walk(op.SelectionSet, queryType, []string{}, ctx)

	// Mirror raw variables as `vars.<name>` so the runtime can use them
	// directly (the iWlz playground UI also shows these alongside where-args).
	for k, v := range variables {
		if !isEmpty(v) {
			ctx.args["vars."+k] = v
		}
	}

	if ctx.fields == nil {
		ctx.fields = []resolvedField{}
	}
	return resolved{Fields: ctx.fields, Args: ctx.args, CoverageUnverifiable: ctx.coverageUnverifiable}
}

type walkCtx struct {
	fields               []resolvedField
	args                 map[string]any
	vars                 map[string]any
	fragMap              map[string]*ast.FragmentDefinition
	fragSeen             map[string]bool // cycle-guard: fragment names already expanded in this branch
	schema               *ast.Schema
	coverageUnverifiable bool
}

func walk(sels ast.SelectionSet, parentType *ast.Definition, pathSegs []string, ctx *walkCtx) {
	if len(pathSegs) > maxDepth {
		ctx.coverageUnverifiable = true
		return
	}
	for _, sel := range sels {
		switch s := sel.(type) {
		case *ast.Field:
			name := s.Name
			segs := append(append([]string(nil), pathSegs...), name)
			var fieldDef *ast.FieldDefinition
			if parentType != nil {
				fieldDef = parentType.Fields.ForName(name)
			}
			scalar := false
			known := false
			var namedType *ast.Definition
			if fieldDef != nil {
				known = true
				namedType = ctx.schema.Types[fieldDef.Type.Name()]
				scalar = namedType != nil && (namedType.Kind == ast.Scalar || namedType.Kind == ast.Enum)
			}
			parentName := "?"
			if parentType != nil {
				parentName = parentType.Name
			}
			ctx.fields = append(ctx.fields, resolvedField{
				Parent: parentName,
				Name:   name,
				Scalar: scalar,
				Known:  known,
				ID:     "Query." + joinDot(segs),
			})
			// Flatten ALL field arguments, prefixed by the argument name. iWlz
			// only flattens `where` because that's the only arg-shape they care
			// about; our queries use `input: {consentId, belastingjaren}` so a
			// generic flatten lets rules bind on `input.consentId` etc.
			for _, arg := range s.Arguments {
				flattenValue([]string{arg.Name}, arg.Value, ctx)
			}
			// Recurse only into a known object/interface; unknown fields surface
			// to the runtime as known=false and get denied via NO_APPLICABLE_RULE.
			if s.SelectionSet != nil && namedType != nil && (namedType.Kind == ast.Object || namedType.Kind == ast.Interface) {
				walk(s.SelectionSet, namedType, segs, ctx)
			}
		case *ast.FragmentSpread:
			frag, ok := ctx.fragMap[s.Name]
			if !ok {
				ctx.coverageUnverifiable = true
				continue
			}
			// Cycle-guard: a fragment that recursively spreads itself
			// (directly or via another fragment) would otherwise blow the
			// stack. Mark it unverifiable rather than expand again.
			if ctx.fragSeen[s.Name] {
				ctx.coverageUnverifiable = true
				continue
			}
			ctx.fragSeen[s.Name] = true
			cond := ctx.schema.Types[frag.TypeCondition]
			if cond == nil {
				cond = parentType
			}
			walk(frag.SelectionSet, cond, pathSegs, ctx)
			delete(ctx.fragSeen, s.Name)
		case *ast.InlineFragment:
			cond := parentType
			if s.TypeCondition != "" {
				if t := ctx.schema.Types[s.TypeCondition]; t != nil {
					cond = t
				}
			}
			walk(s.SelectionSet, cond, pathSegs, ctx)
		}
	}
}

// flattenValue flattens a GraphQL value node into the "where.<path>.<op>"
// key convention used by the iWlz engine for filter-matching. Variables
// resolve from ctx.vars; null/empty variables are dropped (treated as
// "not supplied" so the runtime evaluates filter-key-required correctly).
func flattenValue(prefix []string, v *ast.Value, ctx *walkCtx) {
	if v == nil {
		return
	}
	switch v.Kind {
	case ast.ObjectValue:
		for _, child := range v.Children {
			flattenValue(append(append([]string(nil), prefix...), child.Name), child.Value, ctx)
		}
	case ast.ListValue:
		for i, child := range v.Children {
			flattenValue(append(append([]string(nil), prefix...), itoa(i)), child.Value, ctx)
		}
	case ast.Variable:
		if vv, ok := ctx.vars[v.Raw]; ok && !isEmpty(vv) {
			ctx.args[joinDot(prefix)] = vv
		}
	case ast.NullValue:
		ctx.args[joinDot(prefix)] = nil
	case ast.IntValue, ast.FloatValue, ast.BooleanValue, ast.StringValue, ast.EnumValue, ast.BlockValue:
		ctx.args[joinDot(prefix)] = v.Raw
	}
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return s == ""
	}
	return false
}

func joinDot(s []string) string { return strings.Join(s, ".") }

func itoa(i int) string { return strconv.Itoa(i) }
