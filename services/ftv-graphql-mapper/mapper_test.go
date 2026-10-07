package ftvgraphql

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

const naamQuery = `{ "query": "{ persoon(bsn: \"999990011\") { naam } }" }`

const naamFields = `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "naam"], "parentType": "Persoon", "field": "naam", "leaf": true }
]`

func TestTransport(t *testing.T) {
	setHeader := func(name, value string) func(*Request, *Settings) {
		return func(r *Request, _ *Settings) { r.Headers[name] = value }
	}
	setTarget := func(target string) func(*Request, *Settings) {
		return func(r *Request, _ *Settings) { r.Target = target }
	}
	rejected := failure(SubUnsupportedTransport)
	run(t, []vector{
		{name: "trailing slash", body: naamQuery, edit: setTarget("/graphql/"), fail: rejected},
		{name: "other case", body: naamQuery, edit: setTarget("/GraphQL"), fail: rejected},
		{name: "double slash", body: naamQuery, edit: setTarget("//graphql"), fail: rejected},
		{name: "percent-encoded", body: naamQuery, edit: setTarget("/%67raphql"), fail: rejected},
		{name: "bare question mark", body: naamQuery, edit: setTarget("/graphql?"), fail: rejected},
		{name: "no GraphQL path configured", body: naamQuery, edit: func(_ *Request, s *Settings) { s.GraphQLPath = "" }, fail: rejected},
		{name: "charset utf-8", body: naamQuery, edit: setHeader("Content-Type", "application/json; charset=utf-8"), fields: naamFields},
		{name: "charset in other case, quoted", body: naamQuery, edit: setHeader("Content-Type", `Application/JSON;Charset="UTF-8"`), fields: naamFields},
		{name: "other charset", body: naamQuery, edit: setHeader("Content-Type", "application/json; charset=latin1"), fail: rejected},
		{name: "other parameter", body: naamQuery, edit: setHeader("Content-Type", "application/json; boundary=x"), fail: rejected},
		{name: "graphql-response media type", body: naamQuery, edit: setHeader("Content-Type", "application/graphql-response+json"), fail: rejected},
		// Forms a general media-type parser reads as charset=utf-8; refused here,
		// and by the source through the same function.
		{name: "RFC 2231 charset", body: naamQuery, edit: setHeader("Content-Type", "application/json; charset*=us-ascii''utf-8"), fail: rejected},
		{name: "RFC 2231 continuation", body: naamQuery, edit: setHeader("Content-Type", "application/json; charset*0=utf; charset*1=-8"), fail: rejected},
		{name: "trailing semicolon", body: naamQuery, edit: setHeader("Content-Type", "application/json;"), fail: rejected},
		{name: "space around =", body: naamQuery, edit: setHeader("Content-Type", "application/json ; charset = utf-8"), fail: rejected},
		{name: "header name in lower case", body: naamQuery, edit: func(r *Request, _ *Settings) {
			r.Headers = map[string]any{"content-type": "application/json"}
		}, fields: naamFields},
		{name: "Content-Type twice under two spellings", body: naamQuery, edit: setHeader("content-type", "application/json"), fail: rejected},
		{name: "Content-Type not a string", body: naamQuery, edit: func(r *Request, _ *Settings) {
			r.Headers["Content-Type"] = []any{"application/json"}
		}, fail: rejected},
		{name: "Content-Type absent", body: naamQuery, edit: func(r *Request, _ *Settings) { r.Headers = map[string]any{} }, fail: rejected},
		{name: "Content-Encoding", body: naamQuery, edit: setHeader("Content-Encoding", "gzip"), fail: rejected},
		{name: "empty Upgrade header", body: naamQuery, edit: setHeader("upgrade", ""), fail: rejected},
		{name: "body absent", edit: func(r *Request, _ *Settings) { r.Body = nil }, fail: rejected},
		{name: "body not a string", edit: func(r *Request, _ *Settings) { r.Body = map[string]any{"query": "{ aantalPersonen }"} }, fail: rejected},
	})
}

func TestBody(t *testing.T) {
	invalid := failure(SubInvalidBody)
	run(t, []vector{
		{name: "operationName null", body: `{ "query": "{ aantalPersonen }", "operationName": null }`, fields: aantalFields},
		{name: "operationName empty", body: `{ "query": "{ aantalPersonen }", "operationName": "" }`, fail: invalid},
		{name: "operationName not a string", body: `{ "query": "{ aantalPersonen }", "operationName": 1 }`, fail: invalid},
		{name: "variables null", body: `{ "query": "{ aantalPersonen }", "variables": null }`, fields: aantalFields},
		{name: "variables a string", body: `{ "query": "{ aantalPersonen }", "variables": "{}" }`, fail: invalid},
		{name: "query absent", body: `{ "variables": {} }`, fail: invalid},
		{name: "query not a string", body: `{ "query": ["{ aantalPersonen }"] }`, fail: invalid},
		{name: "empty extensions", body: `{ "query": "{ aantalPersonen }", "extensions": {} }`, fields: aantalFields},
		{name: "extensions null", body: `{ "query": "{ aantalPersonen }", "extensions": null }`, fail: invalid},
		{name: "unlisted extension", body: `{ "query": "{ aantalPersonen }", "extensions": { "trace": true } }`, fail: invalid},
		{name: "listed extension", body: `{ "query": "{ aantalPersonen }", "extensions": { "trace": true } }`,
			edit: func(_ *Request, s *Settings) { s.AllowedExtensions = []string{"trace"} }, fields: aantalFields},
		{name: "duplicate member deep in variables", body: `{ "query": "{ aantalPersonen }", "variables": { "a": { "b": 1, "b": 2 } } }`, fail: invalid},
		{name: "data after the object", body: `{ "query": "{ aantalPersonen }" } {}`, fail: invalid},
		{name: "byte order mark", body: "\uFEFF" + `{ "query": "{ aantalPersonen }" }`, fail: invalid},
		{name: "invalid UTF-8", body: `{ "query": "{ aantalPersonen }", "variables": { "x": "` + "\xff" + `" } }`, fail: invalid},
		{name: "lone high surrogate", body: `{ "query": "{ aantalPersonen }", "variables": { "x": "\ud800" } }`, fail: invalid},
		{name: "lone low surrogate", body: `{ "query": "{ aantalPersonen }", "variables": { "x": "\udc00" } }`, fail: invalid},
		{name: "surrogate pair", body: `{ "query": "{ aantalPersonen }", "variables": { "x": "😀" } }`, fields: aantalFields},
		{name: "escaped backslash before u", body: `{ "query": "{ aantalPersonen }", "variables": { "x": "\\ud800" } }`, fields: aantalFields},
		{name: "JSON nesting over the limit", body: `{ "query": "{ aantalPersonen }", "variables": { "x": [[[[1]]]] } }`,
			edit: func(_ *Request, s *Settings) { s.Limits.NestingDepth = 4 }, fail: failure(SubLimitExceeded)},
		{name: "body over the size limit", body: naamQuery,
			edit: func(_ *Request, s *Settings) { s.Limits.BodyBytes = 10 }, fail: failure(SubLimitExceeded)},
	})
}

const aantalFields = `[ { "path": ["aantalPersonen"], "parentType": "Query", "field": "aantalPersonen", "leaf": true } ]`

func TestCoercion(t *testing.T) {
	// jaren builds a query that declares exactly the variables it uses:
	// $jaar Int (default 2024 unless decl says otherwise), $f InkomenFilter,
	// $l [Int!].
	jaren := func(vars, args string) string {
		decls := map[string]string{"$jaar": "$jaar: Int = 2024", "$f": "$f: InkomenFilter", "$l": "$l: [Int!]"}
		var used []string
		for _, name := range []string{"$jaar", "$f", "$l"} {
			if strings.Contains(args, name) {
				used = append(used, decls[name])
			}
		}
		return `{ "query": "query (` + strings.Join(used, ", ") + `) { persoon(bsn: \"1\") { inkomens(` + args + `) { jaar } } }", "variables": ` + vars + ` }`
	}
	inkomens := func(args string) string {
		return `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false, "args": { "bsn": { "value": "1", "origin": "literal" } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false, "args": ` + args + ` },
  { "path": ["persoon", "inkomens", "jaar"], "parentType": "Inkomen", "field": "jaar", "leaf": true }
]`
	}
	varError := failure(SubVariableError)
	run(t, []vector{
		{name: "Int from an integral float", body: jaren(`{ "jaar": 2024.0 }`, "jaren: [$jaar]"),
			fields: inkomens(`{ "jaren": { "value": [2024], "origin": "mixed", "variables": ["jaar"] } }`)},
		{name: "Int with a fraction", body: jaren(`{ "jaar": 2024.5 }`, "jaren: [$jaar]"), fail: varError},
		{name: "Int over 32 bits", body: jaren(`{ "jaar": 2147483648 }`, "jaren: [$jaar]"), fail: varError},
		{name: "Int from a boolean", body: jaren(`{ "jaar": true }`, "jaren: [$jaar]"), fail: varError},
		{name: "single value coerced to a list", body: jaren(`{ "l": 2024 }`, "jaren: $l"),
			fields: inkomens(`{ "jaren": { "value": [2024], "origin": "variable:l", "variables": ["l"] } }`)},
		{name: "null item in a non-null list", body: jaren(`{ "l": [null] }`, "jaren: $l"), fail: varError},
		{name: "nullable variable without default in a non-null item position", body: `{ "query": "query ($j: Int) { persoon(bsn: \"1\") { inkomens(jaren: [$j]) { jaar } } }" }`,
			fail: &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: SubInvalidQuery, Message: "All Variable Usages Are Allowed at 1:56"}},
		{name: "declared default of an unsupplied variable", body: jaren(`{}`, "jaren: [$jaar]"),
			fields: inkomens(`{ "jaren": { "value": [2024], "origin": "mixed", "variables": ["jaar"] } }`)},
		{name: "explicit null variable", body: jaren(`{ "l": null }`, "jaren: $l"),
			fields: inkomens(`{ "jaren": { "value": null, "origin": "variable:l", "variables": ["l"] } }`)},
		{name: "unsupplied variable used directly falls back to the schema default", body: jaren(`{}`, "jaren: $l"),
			fields: inkomens(`{ "jaren": { "value": [2024], "origin": "schema-default" } }`)},
		{name: "input object from a variable fills the default", body: jaren(`{ "f": { "valuta": "EUR" } }`, "jaren: [2024], filter: $f"),
			fields: inkomens(`{ "jaren": { "value": [2024], "origin": "literal" },
			  "filter": { "value": { "minimum": 0, "valuta": "EUR" }, "origin": "variable:f", "variables": ["f"], "schemaDefaults": [["minimum"]] } }`)},
		{name: "input object with an unknown field", body: jaren(`{ "f": { "maximum": 1 } }`, "jaren: [2024], filter: $f"), fail: varError},
		{name: "unsupplied variable in an input field takes the field default", body: `{ "query": "query ($m: Int) { persoon(bsn: \"1\") { inkomens(jaren: [2024], filter: {minimum: $m, valuta: \"EUR\"}) { jaar } } }" }`,
			fields: inkomens(`{ "jaren": { "value": [2024], "origin": "literal" },
			  "filter": { "value": { "minimum": 0, "valuta": "EUR" }, "origin": "mixed", "variables": ["m"], "schemaDefaults": [["minimum"]] } }`)},
		{name: "unsupplied nullable variable in an input field without default is omitted", body: `{ "query": "query ($v: String) { persoon(bsn: \"1\") { inkomens(jaren: [2024], filter: {minimum: 1, valuta: $v}) { jaar } } }" }`,
			fields: inkomens(`{ "jaren": { "value": [2024], "origin": "literal" },
			  "filter": { "value": { "minimum": 1 }, "origin": "mixed", "variables": ["v"] } }`)},
		{name: "custom scalar passes through as received", body: `{ "query": "query ($b: BSN!) { persoon(bsn: $b) { naam } }", "variables": { "b": 999990011 } }`,
			fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false, "args": { "bsn": { "value": 999990011, "origin": "variable:b", "variables": ["b"] } } },
  { "path": ["persoon", "naam"], "parentType": "Persoon", "field": "naam", "leaf": true }
]`},
		{name: "variables the operation does not define are ignored", body: `{ "query": "{ aantalPersonen }", "variables": { "x": 1 } }`, fields: aantalFields},
	})
}

func TestWalk(t *testing.T) {
	run(t, []vector{
		{name: "fields under @skip and @include still count", body: `{ "query": "{ persoon(bsn: \"999990011\") { naam @skip(if: true) } }" }`, fields: naamFields},
		{name: "inline fragment without type condition sets no on", body: `{ "query": "{ persoon(bsn: \"999990011\") { ... { naam } } }" }`, fields: naamFields},
		{name: "on is not propagated to descendants", body: `{ "query": "{ persoon(bsn: \"999990011\") { ... on Persoon { adres(peildatum: \"x\") { straat } } } }" }`,
			fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false, "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "adres"], "parentType": "Persoon", "on": "Persoon", "field": "adres", "leaf": false,
    "args": { "peildatum": { "value": "x", "origin": "literal" } } },
  { "path": ["persoon", "adres", "straat"], "parentType": "Adres", "field": "straat", "leaf": true }
]`},
		{name: "a field selected twice gives two records", body: `{ "query": "{ persoon(bsn: \"999990011\") { naam naam } }" }`,
			fields: naamFields[:len(naamFields)-1] + `, { "path": ["persoon", "naam"], "parentType": "Persoon", "field": "naam", "leaf": true } ]`},
		{name: "field records over the limit", body: `{ "query": "{ persoon(bsn: \"999990011\") { naam bsn } }" }`,
			edit: func(_ *Request, s *Settings) { s.Limits.FieldRecords = 2 }, fail: failure(SubLimitExceeded)},
		{name: "aliased selections over the limit", body: `{ "query": "{ a: persoon(bsn: \"1\") { naam } b: persoon(bsn: \"1\") { naam } }" }`,
			edit: func(_ *Request, s *Settings) { s.Limits.AliasedSelections = 1 }, fail: failure(SubLimitExceeded)},
		{name: "fragment definitions over the limit", body: `{ "query": "{ persoon(bsn: \"1\") { ...A ...B } } fragment A on Persoon { naam } fragment B on Persoon { bsn }" }`,
			edit: func(_ *Request, s *Settings) { s.Limits.FragmentDefinitions = 1 }, fail: failure(SubLimitExceeded)},
		{name: "GraphQL nesting over the limit", body: `{ "query": "{ persoon(bsn: \"1\") { inkomens(jaren: [2024]) { jaar } } }" }`,
			edit: func(_ *Request, s *Settings) { s.Limits.NestingDepth = 2 }, fail: failure(SubLimitExceeded)},
		{name: "braces in a string do not count as nesting", body: `{ "query": "{ zoek(term: \"{{{{\") { __typename } }" }`,
			edit: func(_ *Request, s *Settings) { s.Limits.NestingDepth = 2 },
			fields: `[
  { "path": ["zoek"], "parentType": "Query", "field": "zoek", "leaf": false, "args": { "term": { "value": "{{{{", "origin": "literal" } } },
  { "path": ["zoek", "__typename"], "parentType": "Zoekresultaat", "field": "__typename", "leaf": true }
]`},
		{name: "subscription", body: `{ "query": "subscription { x }" }`, sdl: "type Query { a: Int } type Subscription { x: Int }",
			fail: &Unverifiable{Code: CodeOperationNotSupported}, operation: `{"type": "subscription", "name": null}`},
		{name: "anonymous next to a named operation", body: `{ "query": "{ aantalPersonen } query B { aantalPersonen }", "operationName": "B" }`,
			fail: &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: SubInvalidQuery, Message: "Lone Anonymous Operation at 1:1"}},
		{name: "validation message has no suggestions", body: `{ "query": "{ persoon(bsn: \"1\") { nam } }" }`,
			fail: &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: SubInvalidQuery, Message: "Field Selections at 1:23"}},
		{name: "deep introspection is valid", body: `{ "query": "{ __schema { types { fields { type { ofType { ofType { ofType { name } } } } } } } }" }`,
			fields: `[
  { "path": ["__schema"], "parentType": "Query", "field": "__schema", "leaf": false },
  { "path": ["__schema", "types"], "parentType": "__Schema", "field": "types", "leaf": false },
  { "path": ["__schema", "types", "fields"], "parentType": "__Type", "field": "fields", "leaf": false,
    "args": { "includeDeprecated": { "value": false, "origin": "schema-default" } } },
  { "path": ["__schema", "types", "fields", "type"], "parentType": "__Field", "field": "type", "leaf": false },
  { "path": ["__schema", "types", "fields", "type", "ofType"], "parentType": "__Type", "field": "ofType", "leaf": false },
  { "path": ["__schema", "types", "fields", "type", "ofType", "ofType"], "parentType": "__Type", "field": "ofType", "leaf": false },
  { "path": ["__schema", "types", "fields", "type", "ofType", "ofType", "ofType"], "parentType": "__Type", "field": "ofType", "leaf": false },
  { "path": ["__schema", "types", "fields", "type", "ofType", "ofType", "ofType", "name"], "parentType": "__Type", "field": "name", "leaf": true }
]`},
	})
}

func TestSchema(t *testing.T) {
	s := loadAppendixA(t)
	if !strings.HasPrefix(s.Digest, "sha256:") || len(s.Digest) != len("sha256:")+64 {
		t.Errorf("digest = %q", s.Digest)
	}
	got := Map(postRequest(naamQuery), s, defaultSettings)
	if got.Schema == nil || got.Schema.Digest != s.Digest {
		t.Errorf("output schema = %+v, want digest %s", got.Schema, s.Digest)
	}
	// The digest is reported even when the request fails before validation.
	got = Map(postRequest(`[]`), s, defaultSettings)
	if got.Schema == nil || got.Unverifiable == nil {
		t.Errorf("want digest and failure, got %+v", got)
	}

	for name, sdl := range map[string]string{
		"executable directive":          "directive @export(as: String) on FIELD\ntype Query { a: Int }",
		"variable definition directive": "directive @v on VARIABLE_DEFINITION\ntype Query { a: Int }",
		"does not parse":                "type Query {",
		"references an undefined type":  "type Query { a: Nope }",
		"no query type":                 "type Persoon { a: Int }",
	} {
		if _, err := LoadSchema(sdl); err == nil {
			t.Errorf("%s: LoadSchema accepted %q", name, sdl)
		}
	}
	if _, err := LoadSchema("directive @tag on FIELD_DEFINITION\ntype Query { a: Int @tag }"); err != nil {
		t.Errorf("type-system directive rejected: %v", err)
	}
}

func TestValidationTimeLimit(t *testing.T) {
	slots := make(chan struct{}, 1)
	release := make(chan struct{})
	if within(slots, 10*time.Millisecond, func() { <-release }) {
		t.Error("slow validation reported as finished")
	}
	// The abandoned validation still holds the only slot.
	if within(slots, 10*time.Millisecond, func() {}) {
		t.Error("validation ran while every slot was taken")
	}
	close(release)
	if !within(slots, time.Second, func() {}) {
		t.Error("slot not released after the abandoned validation finished")
	}
}

func run(t *testing.T, vectors []vector) {
	t.Helper()
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			schema := loadAppendixA(t)
			if v.sdl != "" {
				var err error
				if schema, err = LoadSchema(v.sdl); err != nil {
					t.Fatal(err)
				}
			}
			req, settings := postRequest(v.body), defaultSettings
			if v.edit != nil {
				v.edit(&req, &settings)
			}
			assertOutput(t, Map(req, schema, settings), v)
		})
	}
}

func TestDocument(t *testing.T) {
	invalidAt := func(msg string) *Unverifiable {
		return &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: SubInvalidQuery, Message: msg}
	}
	run(t, []vector{
		{name: "description on an operation", body: `{ "query": "\"Aantal\" query Q { aantalPersonen }" }`,
			fields: aantalFields, operation: `{"type": "query", "name": "Q"}`},
		{name: "block description on a fragment", body: `{ "query": "{ persoon(bsn: \"999990011\") { ...N } } \"\"\"Naam\"\"\" fragment N on Persoon { naam }" }`,
			fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "naam"], "parentType": "Persoon", "on": "Persoon", "field": "naam", "leaf": true }
]`},
		{name: "description on a variable definition", body: `{ "query": "query (\"de persoon\" $b: BSN!, \"\"\"jaar\"\"\" $j: Int = 2024) { persoon(bsn: $b) { inkomens(jaren: [$j]) { jaar } } }", "variables": { "b": "1" } }`,
			fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false, "args": { "bsn": { "value": "1", "origin": "variable:b", "variables": ["b"] } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false, "args": { "jaren": { "value": [2024], "origin": "mixed", "variables": ["j"] } } },
  { "path": ["persoon", "inkomens", "jaar"], "parentType": "Inkomen", "field": "jaar", "leaf": true }
]`},
		{name: "a string default value is not a description", body: `{ "query": "query ($t: String! = \"x\" $u: String! = \"y\") { zoek(term: $t) { __typename } a: zoek(term: $u) { __typename } }" }`,
			fields: `[
  { "path": ["zoek"], "parentType": "Query", "field": "zoek", "leaf": false, "args": { "term": { "value": "x", "origin": "default:t", "variables": ["t"] } } },
  { "path": ["zoek", "__typename"], "parentType": "Zoekresultaat", "field": "__typename", "leaf": true },
  { "path": ["a"], "alias": "a", "parentType": "Query", "field": "zoek", "leaf": false, "args": { "term": { "value": "y", "origin": "default:u", "variables": ["u"] } } },
  { "path": ["a", "__typename"], "parentType": "Zoekresultaat", "field": "__typename", "leaf": true }
]`},
		{name: "positions after a blanked description stay those of the document", body: `{ "query": "\"\"\"ëën\ntwee\"\"\" query Q { nieuwVeld }" }`,
			fail: invalidAt("Field Selections at 2:19")},
		{name: "description on the query shorthand", body: `{ "query": "\"Aantal\" { aantalPersonen }" }`, fail: failure(SubParseError)},
		{name: "description on an extension", body: `{ "query": "\"x\" extend type Persoon { y: Int } { aantalPersonen }" }`, fail: failure(SubParseError)},
		{name: "type definition next to an operation", body: `{ "query": "{ aantalPersonen }\ntype X { a: Int }" }`,
			fail: invalidAt("Executable Definitions at 2:1")},
		{name: "described type definition before an operation", body: `{ "query": "\"t\" type X { a: Int } query { aantalPersonen }" }`,
			fail: invalidAt("Executable Definitions at 1:1")},
		{name: "every kind of type-system definition", body: `{ "query": "schema { query: Query } scalar S @specifiedBy(url: \"u\") interface I implements & J & K @d { a: Int } union U = | A | B enum E { A } input In { a: Int = 1 } directive @d(a: Int) repeatable on | FIELD_DEFINITION | OBJECT extend schema @d extend union U = C query Q { aantalPersonen }" }`,
			fail: invalidAt("Executable Definitions at 1:1")},
		{name: "type named like an operation keyword", body: `{ "query": "type query { a: Int } query { aantalPersonen }" }`,
			fail: invalidAt("Executable Definitions at 1:1")},
		{name: "type-system definitions only", body: `{ "query": "type X { a: Int }" }`, fail: failure(SubNoOperation)},
		{name: "broken type definition", body: `{ "query": "type X { a } { aantalPersonen }" }`, fail: failure(SubParseError)},
		{name: "fields of a type definition are greedy", body: `{ "query": "type X { aantalPersonen }" }`, fail: failure(SubParseError)},
		{name: "directive in a wrong location", body: `{ "query": "query @include(if: true) { aantalPersonen }" }`,
			fail: invalidAt("Directives Are in Valid Locations at 1:8")},
		{name: "unknown input object field", body: `{ "query": "{ persoon(bsn: \"1\") { inkomens(jaren: [1], filter: {maximum: 1}) { jaar } } }" }`,
			fail: invalidAt("Input Object Field Names at 1:53")},
		{name: "required input object field missing", body: `{ "query": "query ($x: AdresVerplicht) { aantalPersonen }" }`,
			sdl:  "input AdresVerplicht { straat: String! } type Query { aantalPersonen(a: AdresVerplicht): Int }",
			fail: invalidAt("All Variables Used at 1:8")},
		{name: "required input object field missing in a literal", body: `{ "query": "{ aantalPersonen(a: {}) }" }`,
			sdl:  "input AdresVerplicht { straat: String! } type Query { aantalPersonen(a: AdresVerplicht): Int }",
			fail: invalidAt("Input Object Required Fields at 1:21")},
		{name: "unknown variable type", body: `{ "query": "query ($x: Nope) { aantalPersonen }" }`,
			fail: invalidAt("Variables Are Input Types at 1:8")},
		{name: "unknown variable type in use", body: `{ "query": "query ($x: Nope) { zoek(term: $x) { __typename } }" }`,
			fail: invalidAt("Variables Are Input Types at 1:8")},
		{name: "unknown fragment type", body: `{ "query": "{ persoon(bsn: \"1\") { ... on Nope { naam } } }" }`,
			fail: invalidAt("Fragment Spread Type Existence at 1:27")},
		{name: "fragment on a scalar", body: `{ "query": "{ persoon(bsn: \"1\") { ... on BSN { naam } } }" }`,
			fail: invalidAt("Fragments on Object, Interface or Union Types at 1:27")},
	})
}

const oneOfSDL = `input Sel @oneOf { a: Int, b: String }
type Query { f(s: Sel): Int, g(s: Sel!): Int }`

func TestOneOf(t *testing.T) {
	varError := failure(SubVariableError)
	run(t, []vector{
		{name: "variable with one field", sdl: oneOfSDL, body: `{ "query": "query ($s: Sel) { f(s: $s) }", "variables": { "s": { "a": 1 } } }`,
			fields: `[ { "path": ["f"], "parentType": "Query", "field": "f", "leaf": true, "args": { "s": { "value": { "a": 1 }, "origin": "variable:s", "variables": ["s"] } } } ]`},
		{name: "variable with two fields", sdl: oneOfSDL, body: `{ "query": "query ($s: Sel) { f(s: $s) }", "variables": { "s": { "a": 1, "b": "x" } } }`, fail: varError},
		{name: "variable with no field", sdl: oneOfSDL, body: `{ "query": "query ($s: Sel) { f(s: $s) }", "variables": { "s": {} } }`, fail: varError},
		{name: "variable with a null field", sdl: oneOfSDL, body: `{ "query": "query ($s: Sel) { f(s: $s) }", "variables": { "s": { "a": null } } }`, fail: varError},
		{name: "literal with one field", sdl: oneOfSDL, body: `{ "query": "{ f(s: {b: \"x\"}) }" }`,
			fields: `[ { "path": ["f"], "parentType": "Query", "field": "f", "leaf": true, "args": { "s": { "value": { "b": "x" }, "origin": "literal" } } } ]`},
		{name: "literal with two fields", sdl: oneOfSDL, body: `{ "query": "{ f(s: {a: 1, b: \"x\"}) }" }`, fail: failure(SubInvalidQuery)},
		{name: "literal with a null field", sdl: oneOfSDL, body: `{ "query": "{ f(s: {a: null}) }" }`, fail: failure(SubInvalidQuery)},
		{name: "literal field from a nullable variable", sdl: oneOfSDL, body: `{ "query": "query ($a: Int) { f(s: {a: $a}) }" }`, fail: failure(SubInvalidQuery)},
		{name: "literal field from a non-null variable", sdl: oneOfSDL, body: `{ "query": "query ($a: Int!) { f(s: {a: $a}) }", "variables": { "a": 3 } }`,
			fields: `[ { "path": ["f"], "parentType": "Query", "field": "f", "leaf": true, "args": { "s": { "value": { "a": 3 }, "origin": "mixed", "variables": ["a"] } } } ]`},
	})
	for name, sdl := range map[string]string{
		"non-null field":     "input Sel @oneOf { a: Int! } type Query { f(s: Sel): Int }",
		"field with default": "input Sel @oneOf { a: Int = 1 } type Query { f(s: Sel): Int }",
	} {
		if _, err := LoadSchema(sdl); err == nil {
			t.Errorf("%s: LoadSchema accepted %q", name, sdl)
		}
	}
}

func TestNullVariableInNonNullPosition(t *testing.T) {
	body := `{ "query": "query ($jaar: Int = 2024) { persoon(bsn: \"1\") { inkomens(jaren: [$jaar]) { jaar } } }", "variables": { "jaar": null } }`
	run(t, []vector{
		{name: "null for a defaulted variable in a non-null item", body: body, fail: failure(SubVariableError)},
		// Variables come before the walk limits (Section 6.8).
		{name: "reported before a walk limit", body: body,
			edit: func(_ *Request, s *Settings) { s.Limits.FieldRecords = 1 }, fail: failure(SubVariableError)},
		{name: "null for a defaulted variable as a non-null argument", body: `{ "query": "query ($b: BSN = \"1\") { persoon(bsn: $b) { naam } }", "variables": { "b": null } }`,
			fail: failure(SubVariableError)},
	})
}

func TestIntegral(t *testing.T) {
	const lo, hi = -2147483648, 2147483647
	for n, want := range map[string]int64{
		"2024": 2024, "2024.0": 2024, "2.024e3": 2024, "2.024E+3": 2024, "20240e-1": 2024,
		"-0": 0, "0.0e99999999999999999999": 0, "-2147483648": lo, "2147483647": hi,
	} {
		if got, ok := integral(json.Number(n), lo, hi); !ok || got != want {
			t.Errorf("integral(%s) = %d, %v; want %d", n, got, ok, want)
		}
	}
	for _, n := range []string{
		"2024.5", "2024.0000000000000001", "2147483648", "-2147483649",
		"1e400", "1e-400", "2.0245e3", "1e99999999999999999999",
		"1e9223372036854775807", "10e9223372036854775807", "1.5e-9223372036854775808",
		"1e1048577", "1e-1048577", "12345678901234567890",
	} {
		if got, ok := integral(json.Number(n), lo, hi); ok {
			t.Errorf("integral(%s) = %d; want no integer", n, got)
		}
	}
	if got, ok := integral("9007199254740993.0", math.MinInt64, math.MaxInt64); !ok || got != 9007199254740993 {
		t.Errorf("integral beyond 2^53 = %d, %v; want exact 9007199254740993", got, ok)
	}
}

func TestExactNumbers(t *testing.T) {
	idSDL := "type Query { a(id: ID): Int }"
	run(t, []vector{
		{name: "Int just above an integer", body: `{ "query": "query ($j: Int!) { persoon(bsn: \"1\") { inkomens(jaren: [$j]) { jaar } } }", "variables": { "j": 2024.0000000000000001 } }`,
			fail: failure(SubVariableError)},
		{name: "ID beyond 2^53 stays exact", sdl: idSDL, body: `{ "query": "query ($i: ID) { a(id: $i) }", "variables": { "i": 9007199254740993.0 } }`,
			fields: `[ { "path": ["a"], "parentType": "Query", "field": "a", "leaf": true, "args": { "id": { "value": "9007199254740993", "origin": "variable:i", "variables": ["i"] } } } ]`},
		{name: "Int with the largest exponent", body: `{ "query": "query ($j: Int!) { persoon(bsn: \"1\") { inkomens(jaren: [$j]) { jaar } } }", "variables": { "j": 1e9223372036854775807 } }`,
			fail: failure(SubVariableError)},
		{name: "ID with the smallest exponent", sdl: idSDL, body: `{ "query": "query ($i: ID) { a(id: $i) }", "variables": { "i": 1.5e-9223372036854775808 } }`,
			fail: failure(SubVariableError)},
		{name: "ID with a fraction", sdl: idSDL, body: `{ "query": "query ($i: ID) { a(id: $i) }", "variables": { "i": 1.5 } }`,
			fail: failure(SubVariableError)},
	})
}

func TestWrittenAliases(t *testing.T) {
	run(t, []vector{
		{name: "alias equal to the field name", body: `{ "query": "{ aantalPersonen: aantalPersonen }" }`,
			fields: `[ { "path": ["aantalPersonen"], "alias": "aantalPersonen", "parentType": "Query", "field": "aantalPersonen", "leaf": true } ]`},
		{name: "aliases equal to the field name count against the limit", body: `{ "query": "{ x: aantalPersonen aantalPersonen: aantalPersonen }" }`,
			edit: func(_ *Request, s *Settings) { s.Limits.AliasedSelections = 1 }, fail: failure(SubLimitExceeded)},
		{name: "in a fragment, next to arguments and directives", body: `{ "query": "{ ...F } fragment F on Query { aantalPersonen: aantalPersonen @skip(if: false) persoon(bsn: \"1\") { inkomens(jaren: [1], filter: {valuta: \"EUR\"}) { jaar } } }" }`,
			fields: `[
  { "path": ["aantalPersonen"], "alias": "aantalPersonen", "parentType": "Query", "on": "Query", "field": "aantalPersonen", "leaf": true },
  { "path": ["persoon"], "parentType": "Query", "on": "Query", "field": "persoon", "leaf": false, "args": { "bsn": { "value": "1", "origin": "literal" } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false,
    "args": { "jaren": { "value": [1], "origin": "literal" }, "filter": { "value": { "minimum": 0, "valuta": "EUR" }, "origin": "literal", "schemaDefaults": [["minimum"]] } } },
  { "path": ["persoon", "inkomens", "jaar"], "parentType": "Inkomen", "field": "jaar", "leaf": true }
]`},
	})
}
