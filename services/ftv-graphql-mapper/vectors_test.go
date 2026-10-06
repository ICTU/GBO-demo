package ftvgraphql

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The test vectors of Appendix A of the FTV GraphQL profile (draft-01). The
// mapper output of every vector is normative for every mapper. Vectors that
// only concern the policy engine (A.25, A.38, A.40) are not here.

func loadAppendixA(t *testing.T) *Schema {
	t.Helper()
	sdl, err := os.ReadFile("testdata/appendix-a.graphql")
	if err != nil {
		t.Fatal(err)
	}
	s, err := LoadSchema(string(sdl))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func postRequest(body string) Request {
	return Request{
		Method:  "POST",
		Target:  "/graphql",
		Headers: map[string]any{"Content-Type": "application/json"},
		Body:    body,
	}
}

var defaultSettings = Settings{GraphQLPath: "/graphql"}

// a1Fields is the mapper output of A.1, as Section 6.2 gives it.
const a1Fields = `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "variable:bsn", "variables": ["bsn"] } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false,
    "args": { "jaren": { "value": [2024], "origin": "mixed", "variables": ["jaar"] } } },
  { "path": ["persoon", "inkomens", "jaar"], "parentType": "Inkomen", "field": "jaar", "leaf": true },
  { "path": ["persoon", "inkomens", "bedrag"], "parentType": "LoonInkomen", "on": "LoonInkomen", "field": "bedrag", "leaf": false },
  { "path": ["persoon", "inkomens", "bedrag", "waarde"], "parentType": "Bedrag", "field": "waarde", "leaf": true },
  { "path": ["persoon", "inkomens", "bedrag", "valuta"], "parentType": "Bedrag", "field": "valuta", "leaf": true }
]`

type vector struct {
	name  string
	body  string
	edit  func(*Request, *Settings)
	sdl   string // replaces the Appendix A schema when set
	noSDL bool   // no schema loaded

	fields    string        // expected fields, as JSON
	operation string        // expected operation, as JSON, when set
	fail      *Unverifiable // expected failure; Message checked when set
}

func TestAppendixAVectors(t *testing.T) {
	for _, v := range appendixA {
		t.Run(v.name, func(t *testing.T) {
			schema := loadAppendixA(t)
			if v.sdl != "" {
				var err error
				if schema, err = LoadSchema(v.sdl); err != nil {
					t.Fatal(err)
				}
			}
			if v.noSDL {
				schema = nil
			}
			req, settings := postRequest(v.body), defaultSettings
			if v.edit != nil {
				v.edit(&req, &settings)
			}
			got := Map(req, schema, settings)
			assertOutput(t, got, v)
		})
	}
}

func assertOutput(t *testing.T, got Output, v vector) {
	t.Helper()
	if got.Profile != Profile {
		t.Errorf("profile = %q", got.Profile)
	}
	if v.fail != nil {
		u := got.Unverifiable
		if u == nil {
			t.Fatalf("want %s/%s, got fields %s", v.fail.Code, v.fail.Subcode, toJSON(t, got.Fields))
		}
		if u.Code != v.fail.Code || u.Subcode != v.fail.Subcode {
			t.Fatalf("want %s/%s, got %s", v.fail.Code, v.fail.Subcode, u)
		}
		if v.fail.Message != "" && u.Message != v.fail.Message {
			t.Errorf("message = %q, want %q", u.Message, v.fail.Message)
		}
		if len(got.Fields) != 0 {
			t.Errorf("fields not empty on failure: %s", toJSON(t, got.Fields))
		}
	} else {
		if got.Unverifiable != nil {
			t.Fatalf("unexpected failure %s", got.Unverifiable)
		}
		assertJSON(t, "fields", got.Fields, v.fields)
	}
	if v.operation != "" {
		assertJSON(t, "operation", got.Operation, v.operation)
	}
}

func assertJSON(t *testing.T, what string, got any, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(toJSON(t, got)), &g); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expected JSON for %s: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, toJSON(t, got), want)
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func failure(subcode string) *Unverifiable {
	return &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: subcode}
}

var appendixA = []vector{
	{
		name:      "A.1 worked example",
		body:      `{"query": "query Inkomen($bsn: BSN!, $jaar: Int = 2024) { persoon(bsn: $bsn) { inkomens(jaren: [$jaar]) { jaar ... on LoonInkomen { bedrag { waarde valuta } } } } }", "variables": { "bsn": "999990011" }}`,
		fields:    a1Fields,
		operation: `{"type": "query", "name": "Inkomen"}`,
	},
	{
		name: "A.2 one field too many",
		body: `{"query": "query Inkomen($bsn: BSN!, $jaar: Int = 2024) { persoon(bsn: $bsn) { inkomens(jaren: [$jaar]) { jaar ... on LoonInkomen { bedrag { waarde valuta } werkgever { naam } } } } }", "variables": { "bsn": "999990011" }}`,
		fields: a1Fields[:len(a1Fields)-1] + `,
  { "path": ["persoon", "inkomens", "werkgever"], "parentType": "LoonInkomen", "on": "LoonInkomen", "field": "werkgever", "leaf": false },
  { "path": ["persoon", "inkomens", "werkgever", "naam"], "parentType": "Werkgever", "field": "naam", "leaf": true }
]`,
	},
	{
		name: "A.3 unknown field",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { nieuwVeld { x } } }" }`,
		fail: &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: SubInvalidQuery, Message: "Field Selections at 1:31"},
	},
	{
		name: "A.4 alias of an uncovered field",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { inkomens(jaren: [2024]) { ... on LoonInkomen { w: werkgever { naam } } } } }" }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false,
    "args": { "jaren": { "value": [2024], "origin": "literal" } } },
  { "path": ["persoon", "inkomens", "w"], "alias": "w", "parentType": "LoonInkomen", "on": "LoonInkomen", "field": "werkgever", "leaf": false },
  { "path": ["persoon", "inkomens", "w", "naam"], "parentType": "Werkgever", "field": "naam", "leaf": true }
]`,
	},
	{
		name: "A.5 two aliases, two persons",
		body: `{ "query": "{ a: persoon(bsn: \"999990011\") { naam } b: persoon(bsn: \"999990022\") { naam } }" }`,
		fields: `[
  { "path": ["a"], "alias": "a", "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["a", "naam"], "parentType": "Persoon", "field": "naam", "leaf": true },
  { "path": ["b"], "alias": "b", "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990022", "origin": "literal" } } },
  { "path": ["b", "naam"], "parentType": "Persoon", "field": "naam", "leaf": true }
]`,
	},
	{
		name: "A.6 existence oracle through a root field",
		body: `{ "query": "{ a: persoon(bsn: \"999990011\") { naam } b: persoon(bsn: \"999990022\") { __typename } }" }`,
		fields: `[
  { "path": ["a"], "alias": "a", "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["a", "naam"], "parentType": "Persoon", "field": "naam", "leaf": true },
  { "path": ["b"], "alias": "b", "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990022", "origin": "literal" } } },
  { "path": ["b", "__typename"], "parentType": "Persoon", "field": "__typename", "leaf": true }
]`,
	},
	{
		name: "A.7 variable default",
		body: `{ "query": "query ($bsn: BSN!, $jaar: Int = 2019) { persoon(bsn: $bsn) { inkomens(jaren: [$jaar]) { jaar } } }", "variables": { "bsn": "999990011" } }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "variable:bsn", "variables": ["bsn"] } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false,
    "args": { "jaren": { "value": [2019], "origin": "mixed", "variables": ["jaar"] } } },
  { "path": ["persoon", "inkomens", "jaar"], "parentType": "Inkomen", "field": "jaar", "leaf": true }
]`,
		operation: `{"type": "query", "name": null}`,
	},
	{
		name: "A.8 variable used directly, resolved to its default",
		body: `{ "query": "query ($bsn: BSN!, $jaren: [Int!] = [2024]) { persoon(bsn: $bsn) { inkomens(jaren: $jaren) { jaar } } }", "variables": { "bsn": "999990011" } }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "variable:bsn", "variables": ["bsn"] } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false,
    "args": { "jaren": { "value": [2024], "origin": "default:jaren", "variables": ["jaren"] } } },
  { "path": ["persoon", "inkomens", "jaar"], "parentType": "Inkomen", "field": "jaar", "leaf": true }
]`,
	},
	{
		name: "A.9 schema argument default",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { inkomens { jaar } } }" }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false,
    "args": { "jaren": { "value": [2024], "origin": "schema-default" } } },
  { "path": ["persoon", "inkomens", "jaar"], "parentType": "Inkomen", "field": "jaar", "leaf": true }
]`,
	},
	{
		name: "A.10 two operations without operationName",
		body: `{ "query": "query A { persoon(bsn: \"999990011\") { naam } } query B { persoon(bsn: \"999990022\") { naam } }" }`,
		fail: failure(SubOperationAmbiguous),
	},
	{
		name: "A.11 two operations with operationName",
		body: `{ "query": "query A { persoon(bsn: \"999990011\") { naam } } query B { persoon(bsn: \"999990022\") { naam } }", "operationName": "B" }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990022", "origin": "literal" } } },
  { "path": ["persoon", "naam"], "parentType": "Persoon", "field": "naam", "leaf": true }
]`,
		operation: `{"type": "query", "name": "B"}`,
	},
	{
		name: "A.12 operationName names no operation",
		body: `{ "query": "query A { persoon(bsn: \"999990011\") { naam } }", "operationName": "Other" }`,
		fail: failure(SubOperationNotFound),
	},
	{
		name:      "A.13 mutation",
		body:      `{ "query": "mutation { wijzigAdres(bsn: \"999990011\", adres: {plaats: \"X\"}) { naam } }" }`,
		fail:      &Unverifiable{Code: CodeOperationNotSupported},
		operation: `{"type": "mutation", "name": null}`,
	},
	{
		name: "A.14 unknown fragment",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { ...Missing } }" }`,
		fail: &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: SubInvalidQuery, Message: "Fragment Spread Target Defined at 1:34"},
	},
	{
		name: "A.15 fragment cycle",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { ...A } } fragment A on Persoon { ...B } fragment B on Persoon { ...A }" }`,
		fail: failure(SubInvalidQuery),
	},
	{
		name: "A.16 parse error",
		body: `{ "query": "{ persoon(bsn: \"999990011\" { naam }" }`,
		fail: failure(SubParseError),
	},
	{
		name: "A.17 no operation",
		body: `{ "query": "fragment N on Persoon { naam }" }`,
		fail: failure(SubNoOperation),
	},
	{
		name: "A.18 depth over limit",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { adres(peildatum: \"2026-01-01\") { straat } } }" }`,
		edit: func(_ *Request, s *Settings) { s.Limits.SelectionDepth = 2 },
		fail: failure(SubLimitExceeded),
	},
	{
		name: "A.19 not a GraphQL body",
		body: `{ "hello": "world" }`,
		fail: failure(SubInvalidBody),
	},
	{
		name: "A.20 batched body",
		body: `[ { "query": "{ persoon(bsn: \"999990011\") { naam } }" } ]`,
		fail: failure(SubInvalidBody),
	},
	{
		name: "A.21 duplicate JSON member",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam } }", "query": "{ persoon(bsn: \"999990022\") { naam adres { straat } } }" }`,
		fail: failure(SubInvalidBody),
	},
	{
		name: "A.22 persisted document member",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam } }", "documentId": "abc" }`,
		fail: failure(SubInvalidBody),
	},
	{
		name: "A.22 extensions.persistedQuery, even when listed",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam } }", "extensions": { "persistedQuery": { "version": 1 } } }`,
		edit: func(_ *Request, s *Settings) { s.AllowedExtensions = []string{"persistedQuery"} },
		fail: failure(SubInvalidBody),
	},
	{
		name: "A.23 query string on the request",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam } }" }`,
		edit: func(r *Request, _ *Settings) {
			r.Target = "/graphql?query=%7B%20persoon(bsn%3A%22999990022%22)%7Bnaam%7D%7D"
		},
		fail: failure(SubUnsupportedTransport),
	},
	{
		name: "A.23 GET",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam } }" }`,
		edit: func(r *Request, _ *Settings) { r.Method = "GET" },
		fail: failure(SubUnsupportedTransport),
	},
	{
		name: "A.23 application/graphql",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam } }" }`,
		edit: func(r *Request, _ *Settings) { r.Headers["Content-Type"] = "application/graphql" },
		fail: failure(SubUnsupportedTransport),
	},
	{
		name: "A.23 Upgrade header",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam } }" }`,
		edit: func(r *Request, _ *Settings) { r.Headers["Upgrade"] = "websocket" },
		fail: failure(SubUnsupportedTransport),
	},
	{
		name:  "A.24 schema missing",
		body:  `{ "query": "{ persoon(bsn: \"999990011\") { naam } }" }`,
		noSDL: true,
		fail:  &Unverifiable{Code: CodeConfigError, Subcode: SubSchemaUnavailable},
	},
	{
		name: "A.26 introspection only",
		body: `{ "query": "{ __schema { types { name } } }" }`,
		fields: `[
  { "path": ["__schema"], "parentType": "Query", "field": "__schema", "leaf": false },
  { "path": ["__schema", "types"], "parentType": "__Schema", "field": "types", "leaf": false },
  { "path": ["__schema", "types", "name"], "parentType": "__Type", "field": "name", "leaf": true }
]`,
	},
	{
		name:   "A.26 __typename alone",
		body:   `{ "query": "{ __typename }" }`,
		fields: `[ { "path": ["__typename"], "parentType": "Query", "field": "__typename", "leaf": true } ]`,
	},
	{
		name: "A.27 argument the rule needs is absent",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { adres { straat } } }" }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "adres"], "parentType": "Persoon", "field": "adres", "leaf": false },
  { "path": ["persoon", "adres", "straat"], "parentType": "Adres", "field": "straat", "leaf": true }
]`,
	},
	{
		name: "A.28 missing non-null variable",
		body: `{ "query": "query ($bsn: BSN!) { persoon(bsn: $bsn) { naam } }", "variables": {} }`,
		fail: failure(SubVariableError),
	},
	{
		name: "A.29 variable coercion failure",
		body: `{ "query": "query ($bsn: BSN!, $jaar: Int = 2024) { persoon(bsn: $bsn) { inkomens(jaren: [$jaar]) { jaar } } }", "variables": { "bsn": "999990011", "jaar": "2024" } }`,
		fail: failure(SubVariableError),
	},
	{
		name: "A.30 named fragment spread twice",
		body: `{ "query": "{ a: persoon(bsn: \"999990011\") { ...N } b: persoon(bsn: \"999990011\") { ...N } } fragment N on Persoon { naam }" }`,
		fields: `[
  { "path": ["a"], "alias": "a", "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["a", "naam"], "parentType": "Persoon", "on": "Persoon", "field": "naam", "leaf": true },
  { "path": ["b"], "alias": "b", "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["b", "naam"], "parentType": "Persoon", "on": "Persoon", "field": "naam", "leaf": true }
]`,
	},
	{
		name: "A.31 same path under two concrete types",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { inkomens(jaren: [2024]) { ... on LoonInkomen { bedrag { waarde } } ... on WinstInkomen { bedrag { valuta } } } } }" }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false,
    "args": { "jaren": { "value": [2024], "origin": "literal" } } },
  { "path": ["persoon", "inkomens", "bedrag"], "parentType": "LoonInkomen", "on": "LoonInkomen", "field": "bedrag", "leaf": false },
  { "path": ["persoon", "inkomens", "bedrag", "waarde"], "parentType": "Bedrag", "field": "waarde", "leaf": true },
  { "path": ["persoon", "inkomens", "bedrag"], "parentType": "WinstInkomen", "on": "WinstInkomen", "field": "bedrag", "leaf": false },
  { "path": ["persoon", "inkomens", "bedrag", "valuta"], "parentType": "Bedrag", "field": "valuta", "leaf": true }
]`,
	},
	{
		name: "A.32 union with __typename only",
		body: `{ "query": "{ zoek(term: \"*\") { __typename } }" }`,
		fields: `[
  { "path": ["zoek"], "parentType": "Query", "field": "zoek", "leaf": false,
    "args": { "term": { "value": "*", "origin": "literal" } } },
  { "path": ["zoek", "__typename"], "parentType": "Zoekresultaat", "field": "__typename", "leaf": true }
]`,
	},
	{
		name:   "A.33 root leaf",
		body:   `{ "query": "{ aantalPersonen }" }`,
		fields: `[ { "path": ["aantalPersonen"], "parentType": "Query", "field": "aantalPersonen", "leaf": true } ]`,
	},
	{
		name: "A.34 disallowed executable directive",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam @export(as: \"n\") } }" }`,
		fail: &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: SubInvalidQuery, Message: "Directives Are Defined at 1:37"},
	},
	{
		name: "A.34 @defer, although the validator library knows it",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { ... @defer { naam } } }" }`,
		fail: &Unverifiable{Code: CodeCoverageUnverifiable, Subcode: SubInvalidQuery, Message: "Directives Are Defined at 1:36"},
	},
	{
		name: "A.35 input-object field default",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { inkomens(jaren: [2024], filter: {valuta: \"EUR\"}) { jaar } } }" }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false,
    "args": {
      "jaren": { "value": [2024], "origin": "literal" },
      "filter": { "value": { "minimum": 0, "valuta": "EUR" }, "origin": "literal", "schemaDefaults": [["minimum"]] } } },
  { "path": ["persoon", "inkomens", "jaar"], "parentType": "Inkomen", "field": "jaar", "leaf": true }
]`,
	},
	{
		name: "A.36 introspection next to a permitted field",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam } __schema { types { name } } }" }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "naam"], "parentType": "Persoon", "field": "naam", "leaf": true },
  { "path": ["__schema"], "parentType": "Query", "field": "__schema", "leaf": false },
  { "path": ["__schema", "types"], "parentType": "__Schema", "field": "types", "leaf": false },
  { "path": ["__schema", "types", "name"], "parentType": "__Type", "field": "name", "leaf": true }
]`,
	},
	{
		name: "A.37 leaf with arguments",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { naam(formaat: \"kort\") } }" }`,
		sdl: `scalar BSN
type Query { persoon(bsn: BSN!): Persoon }
type Persoon { naam(formaat: String): String }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "naam"], "parentType": "Persoon", "field": "naam", "leaf": true,
    "args": { "formaat": { "value": "kort", "origin": "literal" } } }
]`,
	},
	{
		name: "A.39 interface key without type condition",
		body: `{ "query": "{ persoon(bsn: \"999990011\") { inkomens(jaren: [2024]) { jaar bedrag { waarde } } } }" }`,
		fields: `[
  { "path": ["persoon"], "parentType": "Query", "field": "persoon", "leaf": false,
    "args": { "bsn": { "value": "999990011", "origin": "literal" } } },
  { "path": ["persoon", "inkomens"], "parentType": "Persoon", "field": "inkomens", "leaf": false,
    "args": { "jaren": { "value": [2024], "origin": "literal" } } },
  { "path": ["persoon", "inkomens", "jaar"], "parentType": "Inkomen", "field": "jaar", "leaf": true },
  { "path": ["persoon", "inkomens", "bedrag"], "parentType": "Inkomen", "field": "bedrag", "leaf": false },
  { "path": ["persoon", "inkomens", "bedrag", "waarde"], "parentType": "Bedrag", "field": "waarde", "leaf": true }
]`,
	},
}
