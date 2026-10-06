package ftvgraphql

import (
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
	if within(10*time.Millisecond, func() { time.Sleep(time.Second) }) {
		t.Error("slow validation reported as finished")
	}
	if !within(time.Second, func() {}) {
		t.Error("fast validation reported as timed out")
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
