package ftvgraphql

import (
	"fmt"
	"sort"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/lexer"
	"github.com/vektah/gqlparser/v2/validator"
	validatorrules "github.com/vektah/gqlparser/v2/validator/rules"
)

// ruleTitles maps gqlparser's rule names to the section titles of the
// Validation chapter of the GraphQL specification. The INVALID_QUERY
// message carries only the title and the location: never the validator's
// own message, which can suggest names from the schema ("Did you mean").
var ruleTitles = map[string]string{
	"FieldsOnCorrectType":          "Field Selections",
	"FragmentsOnCompositeTypes":    "Fragments On Composite Types",
	"KnownArgumentNames":           "Argument Names",
	"KnownDirectives":              "Directives Are Defined",
	"KnownFragmentNames":           "Fragment Spread Target Defined",
	"KnownRootType":                "Operation Type Existence",
	"KnownTypeNames":               "Fragment Spread Type Existence",
	"LoneAnonymousOperation":       "Lone Anonymous Operation",
	"NoFragmentCycles":             "Fragment Spreads Must Not Form Cycles",
	"NoUndefinedVariables":         "All Variable Uses Defined",
	"NoUnusedFragments":            "Fragments Must Be Used",
	"NoUnusedVariables":            "All Variables Used",
	"OverlappingFieldsCanBeMerged": "Field Selection Merging",
	"PossibleFragmentSpreads":      "Fragment Spread Is Possible",
	"ProvidedRequiredArguments":    "Required Arguments",
	"ScalarLeafs":                  "Leaf Field Selections",
	"SingleFieldSubscriptions":     "Single Root Field",
	"UniqueArgumentNames":          "Argument Uniqueness",
	"UniqueDirectivesPerLocation":  "Directives Are Unique Per Location",
	"UniqueFragmentNames":          "Fragment Name Uniqueness",
	"UniqueInputFieldNames":        "Input Object Field Uniqueness",
	"UniqueOperationNames":         "Operation Name Uniqueness",
	"UniqueVariableNames":          "Variable Uniqueness",
	"ValuesOfCorrectType":          "Values of Correct Type",
	"VariablesAreInputTypes":       "Variables Are Input Types",
	"VariablesInAllowedPosition":   "All Variable Usages Are Allowed",
}

// validationRules are the rules of the specification. gqlparser's
// MaxIntrospectionDepth is not one of them: it rejects valid documents, and
// the walk limits already bound introspection.
func validationRules() *validatorrules.Rules {
	rules := validatorrules.NewDefaultRules()
	rules.RemoveRule("MaxIntrospectionDepth")
	return rules
}

// validate validates the whole document against the bundled schema, within
// the validation time limit (Section 6.4, step 1).
func validate(schema *ast.Schema, doc *ast.QueryDocument, limit time.Duration) *Unverifiable {
	var errs gqlerror.List
	if !within(limit, func() { errs = validator.ValidateWithRules(schema, doc, validationRules()) }) {
		return unverifiable(SubLimitExceeded, "validation time over %s", limit)
	}
	errs = append(errs, undefinedDirectives(doc)...)
	if len(errs) == 0 {
		return nil
	}
	first := earliest(errs)
	title, ok := ruleTitles[first.Rule]
	if !ok {
		title = first.Rule
	}
	if len(first.Locations) == 0 {
		return unverifiable(SubInvalidQuery, "%s", title)
	}
	return unverifiable(SubInvalidQuery, "%s at %d:%d", title, first.Locations[0].Line, first.Locations[0].Column)
}

// within runs fn and reports whether it finished in time. Validation cannot
// be cancelled: on a timeout the goroutine finishes on its own, and its
// result is discarded.
func within(limit time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// earliest picks the error closest to the start of the document, so the
// reported rule does not depend on the validator's rule order.
func earliest(errs gqlerror.List) *gqlerror.Error {
	sorted := append(gqlerror.List(nil), errs...)
	pos := func(e *gqlerror.Error) (int, int) {
		if len(e.Locations) == 0 {
			return int(^uint(0) >> 1), 0
		}
		return e.Locations[0].Line, e.Locations[0].Column
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		li, ci := pos(sorted[i])
		lj, cj := pos(sorted[j])
		if li != lj {
			return li < lj
		}
		return ci < cj
	})
	return sorted[0]
}

// undefinedDirectives reports every executable directive other than @skip
// and @include. gqlparser's prelude also defines @defer, so Directives Are
// Defined alone would let it through; the bundled SDL defines no executable
// directive of its own (LoadSchema), so the set must be exactly these two.
func undefinedDirectives(doc *ast.QueryDocument) gqlerror.List {
	var errs gqlerror.List
	check := func(dirs ast.DirectiveList) {
		for _, d := range dirs {
			if d.Name == "skip" || d.Name == "include" {
				continue
			}
			e := &gqlerror.Error{Rule: "KnownDirectives", Message: fmt.Sprintf("@%s", d.Name)}
			if d.Position != nil {
				e.Locations = []gqlerror.Location{{Line: d.Position.Line, Column: d.Position.Column}}
			}
			errs = append(errs, e)
		}
	}
	var selections func(ast.SelectionSet)
	selections = func(set ast.SelectionSet) {
		for _, sel := range set {
			switch s := sel.(type) {
			case *ast.Field:
				check(s.Directives)
				selections(s.SelectionSet)
			case *ast.InlineFragment:
				check(s.Directives)
				selections(s.SelectionSet)
			case *ast.FragmentSpread:
				check(s.Directives)
			}
		}
	}
	for _, op := range doc.Operations {
		check(op.Directives)
		for _, v := range op.VariableDefinitions {
			check(v.Directives)
		}
		selections(op.SelectionSet)
	}
	for _, f := range doc.Fragments {
		check(f.Directives)
		selections(f.SelectionSet)
	}
	return errs
}

// queryNestingDepth is the deepest nesting of selection sets, list values
// and object values in the document, read from its tokens so that braces in
// strings and comments do not count. A lexer error stops the scan; the
// parser reports it.
func queryNestingDepth(query string) int {
	lex := lexer.New(&ast.Source{Input: query})
	depth, deepest := 0, 0
	for {
		tok, err := lex.ReadToken()
		if err != nil || tok.Kind == lexer.EOF {
			return deepest
		}
		switch tok.Kind {
		case lexer.BraceL, lexer.BracketL:
			depth++
			deepest = max(deepest, depth)
		case lexer.BraceR, lexer.BracketR:
			depth--
		}
	}
}
