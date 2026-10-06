package ftvgraphql

import (
	"github.com/vektah/gqlparser/v2/ast"
)

// walk emits one Field per selection of the operation, in document order,
// after fragment expansion (Section 6.4, steps 2 to 10). It runs on a
// validated document, so every name it looks up exists; where one does not,
// it fails closed rather than guess.
func walk(schema *ast.Schema, doc *ast.QueryDocument, op *ast.OperationDefinition, vars map[string]variable, limits Limits) ([]Field, *Unverifiable) {
	root := schema.Query
	w := &walker{schema: schema, doc: doc, vars: vars, limits: limits, fields: []Field{}}
	if fail := w.selections(op.SelectionSet, root, nil, ""); fail != nil {
		return nil, fail
	}
	return w.fields, nil
}

type walker struct {
	schema  *ast.Schema
	doc     *ast.QueryDocument
	vars    map[string]variable
	limits  Limits
	fields  []Field
	aliases int
}

// selections walks a selection set on parent. on is the type condition of
// the fragment this set belongs to, empty when it belongs to none.
func (w *walker) selections(set ast.SelectionSet, parent *ast.Definition, path []string, on string) *Unverifiable {
	for _, sel := range set {
		var fail *Unverifiable
		switch s := sel.(type) {
		case *ast.Field:
			fail = w.field(s, parent, path, on)
		case *ast.InlineFragment:
			// No type condition: the fragment adds nothing, and its
			// children get no on.
			t, cond := parent, ""
			if s.TypeCondition != "" {
				t, cond = w.schema.Types[s.TypeCondition], s.TypeCondition
			}
			fail = w.selections(s.SelectionSet, t, path, cond)
		case *ast.FragmentSpread:
			// A fragment spread twice is walked twice: once per position.
			f := w.doc.Fragments.ForName(s.Name)
			if f == nil {
				return unverifiable(SubInvalidQuery, "Fragment Spread Target Defined")
			}
			fail = w.selections(f.SelectionSet, w.schema.Types[f.TypeCondition], path, f.TypeCondition)
		}
		if fail != nil {
			return fail
		}
	}
	return nil
}

func (w *walker) field(s *ast.Field, parent *ast.Definition, path []string, on string) *Unverifiable {
	if parent == nil {
		return unverifiable(SubInvalidQuery, "Field Selections")
	}
	// The response key: gqlparser sets Alias to the name when none is
	// written, so `naam: naam` reads as no alias. Both give the same key.
	fieldPath := append(append(make([]string, 0, len(path)+1), path...), s.Alias)
	rec := Field{Path: fieldPath, ParentType: parent.Name, On: on, Field: s.Name, Leaf: len(s.SelectionSet) == 0}
	if s.Alias != s.Name {
		rec.Alias = s.Alias
	}
	if fail := w.checkLimits(rec); fail != nil {
		return fail
	}
	// @skip and @include are ignored: a field that may be skipped still
	// counts (Section 6.4, step 8).
	if s.Name == "__typename" {
		w.fields = append(w.fields, rec)
		return nil
	}
	def := parent.Fields.ForName(s.Name)
	if def == nil {
		return unverifiable(SubInvalidQuery, "Field Selections")
	}
	args, fail := w.args(def, s)
	if fail != nil {
		return fail
	}
	rec.Args = args
	w.fields = append(w.fields, rec)
	if rec.Leaf {
		return nil
	}
	return w.selections(s.SelectionSet, w.schema.Types[def.Type.Name()], fieldPath, "")
}

// checkLimits applies the walk limits at each emission, so fragment fan-out
// stops at the first record over the limit.
func (w *walker) checkLimits(rec Field) *Unverifiable {
	if len(rec.Path) > w.limits.SelectionDepth {
		return unverifiable(SubLimitExceeded, "selection depth over %d", w.limits.SelectionDepth)
	}
	if len(w.fields)+1 > w.limits.FieldRecords {
		return unverifiable(SubLimitExceeded, "field records over %d", w.limits.FieldRecords)
	}
	if rec.Alias != "" {
		w.aliases++
		if w.aliases > w.limits.AliasedSelections {
			return unverifiable(SubLimitExceeded, "aliased selections over %d", w.limits.AliasedSelections)
		}
	}
	return nil
}

// args resolves every argument the field defines. There is no global
// argument map: two selections of one field keep their own (Section 6.5).
func (w *walker) args(def *ast.FieldDefinition, s *ast.Field) (map[string]Arg, *Unverifiable) {
	var args map[string]Arg
	for _, ad := range def.Arguments {
		var written *ast.Value
		if a := s.Arguments.ForName(ad.Name); a != nil {
			written = a.Value
		}
		arg, ok, err := argument(w.schema, w.vars, ad, written)
		if err != nil {
			line, col := 0, 0
			if written != nil && written.Position != nil {
				line, col = written.Position.Line, written.Position.Column
			}
			return nil, unverifiable(SubInvalidQuery, "Values of Correct Type at %d:%d", line, col)
		}
		if !ok {
			continue
		}
		if args == nil {
			args = map[string]Arg{}
		}
		args[ad.Name] = arg
	}
	return args, nil
}
