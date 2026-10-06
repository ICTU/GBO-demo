package ftvgraphql

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func catalogFS(t *testing.T, manifest string, extra map[string]string) fstest.MapFS {
	t.Helper()
	sdl, err := os.ReadFile("testdata/appendix-a.graphql")
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{
		ManifestFile:              {Data: []byte(manifest)},
		"schemas/persoon.graphql": {Data: sdl},
	}
	for name, data := range extra {
		fsys[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return fsys
}

func TestCatalog(t *testing.T) {
	fsys := catalogFS(t, `{ "services": {
		"persoon": { "path": "/graphql", "schema": "schemas/persoon.graphql" },
		"kapot":   { "path": "/graphql", "schema": "kapot.graphql" },
		"zoek":    { "path": "/graphql", "schema": "bestaat-niet.graphql" },
		"leeg":    { "path": "", "schema": "schemas/persoon.graphql" }
	} }`, map[string]string{"kapot.graphql": "type Query {"})
	c, err := LoadCatalog(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.Names(), ","); got != "kapot,leeg,persoon,zoek" {
		t.Errorf("names = %s", got)
	}
	s, ok := c.Service("persoon")
	if !ok || s.Err != nil || s.Schema == nil || s.Path != "/graphql" {
		t.Fatalf("persoon = %+v, %v", s, ok)
	}
	if s.Schema.Digest != loadAppendixA(t).Digest {
		t.Error("digest differs from LoadSchema of the same file")
	}
	for _, name := range []string{"kapot", "zoek", "leeg"} {
		if s, _ := c.Service(name); s.Err == nil || s.Schema != nil {
			t.Errorf("%s: want a load error, got %+v", name, s)
		}
	}

	t.Run("maps a request for a known service", func(t *testing.T) {
		assertOutput(t, c.Map("persoon", postRequest(naamQuery)), vector{fields: naamFields})
	})
	t.Run("a service whose SDL failed fails closed", func(t *testing.T) {
		got := c.Map("kapot", postRequest(naamQuery))
		assertOutput(t, got, vector{fail: &Unverifiable{Code: CodeConfigError, Subcode: SubSchemaUnavailable}})
		if got.Schema != nil {
			t.Errorf("schema = %+v, want null", got.Schema)
		}
	})
	t.Run("an unknown service has no schema", func(t *testing.T) {
		got := c.Map("onbekend", postRequest(naamQuery))
		assertOutput(t, got, vector{fail: &Unverifiable{Code: CodeConfigError, Subcode: SubSchemaUnavailable,
			Message: `no GraphQL schema for service "onbekend"`}})
	})
	t.Run("a catalog that did not load fails closed", func(t *testing.T) {
		var none *Catalog
		assertOutput(t, none.Map("persoon", postRequest(naamQuery)), vector{fail: &Unverifiable{Code: CodeConfigError, Subcode: SubSchemaUnavailable}})
	})
}

func TestCatalogManifest(t *testing.T) {
	for name, manifest := range map[string]string{
		"not JSON":      `{ "services": `,
		"unknown field": `{ "services": { "persoon": { "path": "/graphql", "schema": "schemas/persoon.graphql", "limits": {} } } }`,
		"no services":   `{ "services": {} }`,
	} {
		if _, err := LoadCatalog(catalogFS(t, manifest, nil)); err == nil {
			t.Errorf("%s: LoadCatalog accepted %s", name, manifest)
		}
	}
	if _, err := LoadCatalog(fstest.MapFS{}); err == nil {
		t.Error("LoadCatalog accepted a directory without a manifest")
	}
}
