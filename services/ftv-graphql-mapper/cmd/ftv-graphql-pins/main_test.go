package main

import (
	"strings"
	"testing"
	"testing/fstest"

	ftvgraphql "gbo-demo/ftv-graphql-mapper"
)

func TestRender(t *testing.T) {
	plain := "type Query { a: Int }"
	persons := `scalar BSN
type Query { persoon(bsn: BSN!): P }
interface I { bsn: BSN! }
type P implements I { bsn: BSN! ouders: [BSN!] naam: String }`
	digest := func(sdl string) string {
		schema, err := ftvgraphql.LoadSchema(sdl)
		if err != nil {
			t.Fatal(err)
		}
		return schema.Digest
	}
	catalog, err := ftvgraphql.LoadCatalog(fstest.MapFS{
		ftvgraphql.ManifestFile: {Data: []byte(`{ "services": {
			"zoek":    { "path": "/graphql", "schema": "a.graphql" },
			"persoon": { "path": "/graphql", "schema": "p.graphql" } } }`)},
		"a.graphql": {Data: []byte(plain)},
		"p.graphql": {Data: []byte(persons)},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := render(catalog)
	if err != nil {
		t.Fatal(err)
	}
	digests := "digests := {\n" +
		"\t\"persoon\": \"" + digest(persons) + "\",\n" +
		"\t\"zoek\": \"" + digest(plain) + "\",\n" +
		"}\n"
	if !strings.Contains(got, digests) {
		t.Errorf("render:\n%s\nwant:\n%s", got, digests)
	}
	// The argument bsn is no field; a list of BSNs is.
	bsnFields := "bsn_fields := {\n" +
		"\t\"persoon\": {\"I.bsn\", \"P.bsn\", \"P.ouders\"},\n" +
		"\t\"zoek\": set(),\n" +
		"}\n"
	if !strings.HasSuffix(got, bsnFields) {
		t.Errorf("render:\n%s\nwant suffix:\n%s", got, bsnFields)
	}
	if !strings.Contains(got, "package gbo.graphql_schemas\n") {
		t.Errorf("render has no package line:\n%s", got)
	}
}

func TestRenderRefusesABrokenSchema(t *testing.T) {
	catalog, err := ftvgraphql.LoadCatalog(fstest.MapFS{
		ftvgraphql.ManifestFile: {Data: []byte(`{ "services": { "kapot": { "path": "/graphql", "schema": "k.graphql" } } }`)},
		"k.graphql":             {Data: []byte("type Query {")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := render(catalog); err == nil {
		t.Errorf("render accepted a broken schema:\n%s", out)
	}
}
