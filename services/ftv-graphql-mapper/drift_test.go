package ftvgraphql

import (
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

const driftCopy = `
type Query { persoon(bsn: String!, jaren: [Int!] = [2024]): Persoon, zoek(filter: Filter): [Gegeven!]! }
type Persoon implements Gegeven { naam: String, status: Status, aangiften: [Aangifte!]! }
type Bedrijf implements Gegeven { naam: String }
interface Gegeven { naam: String }
type Aangifte { jaar: Int }
type Verklaring { jaar: Int }
union Stuk = Aangifte | Verklaring
input Filter { naam: String, max: Int = 10 }
enum Status { ACTIEF, OVERLEDEN }
scalar BSN
type Los { x: Int }
`

// driftAgainst compares the copy above with a source built from it by edit,
// both read the same way, so only the edit can differ.
func driftAgainst(t *testing.T, edit func(string) string) []string {
	t.Helper()
	bundled, err := gqlparser.LoadSchema(&ast.Source{Name: "copy.graphql", Input: driftCopy})
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	source, err := gqlparser.LoadSchema(&ast.Source{Name: "source.graphql", Input: edit(driftCopy)})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	return drift(modelFromAST(bundled), modelFromAST(source))
}

func replace(old, new string) func(string) string {
	return func(s string) string {
		if !strings.Contains(s, old) {
			panic("edit does not apply: " + old)
		}
		return strings.Replace(s, old, new, 1)
	}
}

func TestDrift(t *testing.T) {
	cases := []struct {
		name string
		edit func(string) string
		want []string
	}{
		{"identical", func(s string) string { return s }, nil},
		{"description and directive only", replace(`type Aangifte { jaar: Int }`, `"Een aangifte" type Aangifte { jaar: Int @deprecated(reason: "x") }`), nil},
		{"extra type in source", func(s string) string { return s + "type Extra { x: Int }\n" }, nil},
		{"extra field in source", replace(`type Aangifte { jaar: Int }`, `type Aangifte { jaar: Int, bedrag: Int }`), nil},
		{"default written differently", replace(`= [2024]`, `= [ 2024 ]`), nil},

		{"type missing", replace(`type Los { x: Int }`, ``), []string{"type Los: missing in source"}},
		{"other kind", replace(`scalar BSN`, `enum BSN { X }`), []string{"type BSN: SCALAR in copy, ENUM in source"}},
		{"field missing", replace(`type Aangifte { jaar: Int }`, `type Aangifte { belastingjaar: Int }`), []string{"Aangifte.jaar: missing in source"}},
		{"other return type", replace(`type Aangifte { jaar: Int }`, `type Aangifte { jaar: String }`), []string{"Aangifte.jaar: returns Int in copy, String in source"}},
		{"other wrappers", replace(`aangiften: [Aangifte!]!`, `aangiften: [Aangifte]`), []string{"Persoon.aangiften: returns [Aangifte!]! in copy, [Aangifte] in source"}},
		{"extra argument", replace(`persoon(bsn: String!,`, `persoon(bsn: String!, peildatum: String,`), []string{"Query.persoon(peildatum): argument only in source"}},
		{"argument missing", replace(`persoon(bsn: String!, jaren: [Int!] = [2024])`, `persoon(bsn: String!)`), []string{"Query.persoon(jaren): argument missing in source"}},
		{"argument of other type", replace(`persoon(bsn: String!,`, `persoon(bsn: String,`), []string{"Query.persoon(bsn): argument of type String! in copy, String in source"}},
		{"other default", replace(`= [2024]`, `= [2025]`), []string{"Query.persoon(jaren): default [2024] in copy, [2025] in source"}},
		{"default only in source", replace(`persoon(bsn: String!,`, `persoon(bsn: String! = "1",`), []string{`Query.persoon(bsn): default none in copy, "1" in source`}},
		{"default only in copy", replace(`= [2024]`, ``), []string{"Query.persoon(jaren): default [2024] in copy, none in source"}},
		{"extra input field", replace(`input Filter { naam: String,`, `input Filter { naam: String, bsn: String,`), []string{"Filter(bsn): input field only in source"}},
		{"input field missing", replace(`input Filter { naam: String,`, `input Filter {`), []string{"Filter(naam): input field missing in source"}},
		{"input field of other type", replace(`max: Int = 10`, `max: String = "10"`), []string{"Filter(max): default 10 in copy, \"10\" in source", "Filter(max): input field of type Int in copy, String in source"}},
		{"input field default", replace(`max: Int = 10`, `max: Int = 100`), []string{"Filter(max): default 10 in copy, 100 in source"}},
		{"extra implementing type", func(s string) string { return s + "type Stichting implements Gegeven { naam: String }\n" }, []string{"type Gegeven: members [Bedrijf, Persoon] in copy, [Bedrijf, Persoon, Stichting] in source"}},
		{"extra union member", replace(`union Stuk = Aangifte | Verklaring`, `union Stuk = Aangifte | Verklaring | Persoon`), []string{"type Stuk: members [Aangifte, Verklaring] in copy, [Aangifte, Persoon, Verklaring] in source"}},
		{"extra enum value", replace(`enum Status { ACTIEF, OVERLEDEN }`, `enum Status { ACTIEF, OVERLEDEN, GEEMIGREERD }`), []string{"enum Status: values [ACTIEF, OVERLEDEN] in copy, [ACTIEF, GEEMIGREERD, OVERLEDEN] in source"}},
		{"other query root", func(s string) string { return s + "schema { query: Root }\ntype Root { x: Int }\n" }, []string{"query root type: Query in copy, Root in source"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := driftAgainst(t, tc.edit)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("drift:\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// A source whose library cannot declare one interface implementing another
// lets its objects implement both. The objects are the members on both sides.
func TestAnInterfaceImplementingAnotherAddsNoMember(t *testing.T) {
	bundled, err := gqlparser.LoadSchema(&ast.Source{Input: `type Query { p: Partij }
interface Partij { naam: String }
interface Persoon implements Partij { naam: String }
type Ingezetene implements Persoon & Partij { naam: String }`})
	if err != nil {
		t.Fatal(err)
	}
	source, err := gqlparser.LoadSchema(&ast.Source{Input: `type Query { p: Partij }
interface Partij { naam: String }
interface Persoon { naam: String }
type Ingezetene implements Persoon & Partij { naam: String }`})
	if err != nil {
		t.Fatal(err)
	}
	if d := drift(modelFromAST(bundled), modelFromAST(source)); len(d) != 0 {
		t.Fatalf("drift = %q, want none", d)
	}
}

// A source's introspection reads to the same model as the equivalent SDL.
func TestIntrospectionMatchesTheCopy(t *testing.T) {
	copy, err := LoadSchema(`type Query { persoon(bsn: String!, jaren: [Int!] = [2024]): Persoon }
type Persoon { naam: String }`)
	if err != nil {
		t.Fatal(err)
	}
	source, err := ParseIntrospection([]byte(`{"data":{"__schema":{
  "queryType":{"name":"Query"},"mutationType":null,"subscriptionType":null,
  "types":[
    {"kind":"OBJECT","name":"Query","fields":[{"name":"persoon","args":[
      {"name":"bsn","defaultValue":null,"type":{"kind":"NON_NULL","name":null,"ofType":{"kind":"SCALAR","name":"String","ofType":null}}},
      {"name":"jaren","defaultValue":"[2024]","type":{"kind":"LIST","name":null,"ofType":{"kind":"NON_NULL","name":null,"ofType":{"kind":"SCALAR","name":"Int","ofType":null}}}}],
      "type":{"kind":"OBJECT","name":"Persoon","ofType":null}}],"inputFields":null,"possibleTypes":null,"enumValues":null},
    {"kind":"OBJECT","name":"Persoon","fields":[{"name":"naam","args":[],"type":{"kind":"SCALAR","name":"String","ofType":null}}],"inputFields":null,"possibleTypes":null,"enumValues":null},
    {"kind":"SCALAR","name":"String","fields":null,"inputFields":null,"possibleTypes":null,"enumValues":null},
    {"kind":"OBJECT","name":"__Schema","fields":[],"inputFields":null,"possibleTypes":null,"enumValues":null}
  ]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if d := copy.Drift(source); len(d) != 0 {
		t.Fatalf("drift = %q, want none", d)
	}
}

// An introspection that failed, or did not happen, vouches for nothing.
func TestAnUnusableIntrospectionIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"errors":      `{"data":null,"errors":[{"message":"introspection disabled"}]}`,
		"no schema":   `{"data":{}}`,
		"not JSON":    `<html>`,
		"bad default": `{"__schema":{"queryType":{"name":"Query"},"types":[{"kind":"OBJECT","name":"Query","fields":[{"name":"f","args":[{"name":"a","defaultValue":"[","type":{"kind":"SCALAR","name":"Int"}}],"type":{"kind":"SCALAR","name":"Int"}}]}]}}`,
	} {
		if _, err := ParseIntrospection([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
