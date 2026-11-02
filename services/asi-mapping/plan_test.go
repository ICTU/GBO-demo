package asimapping

import (
	"bytes"
	"sort"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
	"github.com/vektah/gqlparser/v2/parser"
)

// canonical formats a query with every selection set sorted, so a test can
// state the expected query in any order.
func canonical(t *testing.T, document string) string {
	t.Helper()
	parsed, err := parser.ParseQuery(&ast.Source{Input: document})
	if err != nil {
		t.Fatalf("parse %q: %v", document, err)
	}
	for _, operation := range parsed.Operations {
		sortSelections(operation.SelectionSet)
	}
	var out bytes.Buffer
	formatter.NewFormatter(&out).FormatQueryDocument(parsed)
	return out.String()
}

func sortSelections(set ast.SelectionSet) {
	key := func(selection ast.Selection) string {
		switch s := selection.(type) {
		case *ast.Field:
			return s.Alias
		case *ast.InlineFragment:
			return "... on " + s.TypeCondition
		case *ast.FragmentSpread:
			return "..." + s.Name
		}
		return ""
	}
	sort.SliceStable(set, func(i, j int) bool { return key(set[i]) < key(set[j]) })
	for _, selection := range set {
		switch s := selection.(type) {
		case *ast.Field:
			sortSelections(s.SelectionSet)
		case *ast.InlineFragment:
			sortSelections(s.SelectionSet)
		}
	}
}

const (
	familyName  = "https://gbo.example/attributes/family_name/1"
	givenName   = "https://gbo.example/attributes/given_name/1"
	nationality = "https://gbo.example/attributes/nationality/1"
	address     = "https://gbo.example/attributes/address/1"
)

// query is the BRP's document with selection in place of ...Selected.
func query(selection string) string {
	return "query Asi($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { " + selection + " } }"
}

func TestPlan(t *testing.T) {
	capability := brpCapability(t)

	cases := []struct {
		name     string
		requests []Request
		want     string
	}{
		{"Q1 one attribute", []Request{{Identifier: givenName}},
			query("voornamen")},
		{"Q2 an attribute on a type", []Request{{Identifier: address}},
			query("... on Ingezetene { woontOp { straatnaam huisnummer huisletter huisnummertoevoeging postcode woonplaatsnaam } }")},
		{"Q3 two attributes in one query", []Request{{Identifier: familyName}, {Identifier: nationality}},
			query("voorvoegsel geslachtsnaam heeftNationaliteit { nationaliteit }")},
		{"Q4 a fragment asks only its member", []Request{{Identifier: address, Members: []string{"city"}}},
			query("... on Ingezetene { woontOp { woonplaatsnaam } }")},
		{"Q5 attribute and fragment ask each field once", []Request{{Identifier: address}, {Identifier: address, Members: []string{"city"}}},
			query("... on Ingezetene { woontOp { straatnaam huisnummer huisletter huisnummertoevoeging postcode woonplaatsnaam } }")},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			document, err := capability.Plan(test.requests)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := canonical(t, document), canonical(t, test.want); got != want {
				t.Fatalf("Plan() =\n%s\nwant\n%s", got, want)
			}
		})
	}

	t.Run("Q6 a fragment on a const member asks nothing", func(t *testing.T) {
		document, err := capability.Plan([]Request{{Identifier: address, Members: []string{"country"}}})
		if err != nil {
			t.Fatal(err)
		}
		if document != "" {
			t.Fatalf("Plan() = %q, want no query", document)
		}
	})

	t.Run("an unknown identifier or member", func(t *testing.T) {
		if _, err := capability.Plan([]Request{{Identifier: "https://gbo.example/attributes/unknown/1"}}); err == nil {
			t.Error("Plan() accepted an unknown identifier")
		}
		if _, err := capability.Plan([]Request{{Identifier: address, Members: []string{"county"}}}); err == nil {
			t.Error("Plan() accepted an unknown member")
		}
	})
}
