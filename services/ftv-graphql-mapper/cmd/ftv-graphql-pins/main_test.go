package main

import (
	"strings"
	"testing"
	"testing/fstest"

	ftvgraphql "gbo-demo/ftv-graphql-mapper"
)

func TestRender(t *testing.T) {
	sdl := "type Query { a: Int }"
	schema, err := ftvgraphql.LoadSchema(sdl)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ftvgraphql.LoadCatalog(fstest.MapFS{
		ftvgraphql.ManifestFile: {Data: []byte(`{ "services": {
			"zoek":    { "path": "/graphql", "schema": "a.graphql" },
			"persoon": { "path": "/graphql", "schema": "a.graphql" } } }`)},
		"a.graphql": {Data: []byte(sdl)},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := render(catalog)
	if err != nil {
		t.Fatal(err)
	}
	want := "digests := {\n" +
		"\t\"persoon\": \"" + schema.Digest + "\",\n" +
		"\t\"zoek\": \"" + schema.Digest + "\",\n" +
		"}\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("render:\n%s\nwant suffix:\n%s", got, want)
	}
	if !strings.Contains(got, "package dvtp.gbo.graphql_schemas\n") {
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
