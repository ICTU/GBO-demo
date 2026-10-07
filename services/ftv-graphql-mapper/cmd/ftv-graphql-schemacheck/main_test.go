package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// catalog writes a one-service catalog whose copy is sdl.
func catalog(t *testing.T, sdl string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"services.json": `{"services":{"bri":{"path":"/graphql","schema":"bd.graphql"}}}`,
		"bd.graphql":    sdl,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const introspection = `{"data":{"__schema":{"queryType":{"name":"Query"},"types":[
  {"kind":"OBJECT","name":"Query","fields":[{"name":"jaar","args":[],"type":{"kind":"SCALAR","name":"Int"}}]},
  {"kind":"SCALAR","name":"Int"}]}}}`

// Like graphql-go: introspection takes includeDeprecated on fields only.
const legacyCapabilities = `{"data":{"field":{"fields":[{"name":"args","args":[]}]},
  "type":{"fields":[{"name":"inputFields","args":[]}]}}}`

const modernCapabilities = `{"data":{"field":{"fields":[{"name":"args","args":[{"name":"includeDeprecated"}]}]},
  "type":{"fields":[{"name":"inputFields","args":[{"name":"includeDeprecated"}]}]}}}`

// source answers the capability query with caps and the introspection with
// status and body; it records the introspection query it got.
func source(t *testing.T, status int, body string) string {
	t.Helper()
	url, _ := sourceWith(t, legacyCapabilities, status, body)
	return url
}

func sourceWith(t *testing.T, caps string, status int, body string) (string, *string) {
	t.Helper()
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "transport", http.StatusBadRequest)
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "FTVSchemaCheckCapabilities") {
			_, _ = w.Write([]byte(caps))
			return
		}
		asked = req.Query
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/graphql", &asked
}

// A source that can hide deprecated arguments and input fields is asked for
// them.
func TestSchemaCheckAsksForDeprecatedElementsWhereTheyCanHide(t *testing.T) {
	url, asked := sourceWith(t, modernCapabilities, 200, introspection)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-catalog", catalog(t, "type Query { jaar: Int }"), "-service", "bri", "-url", url}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, &stderr)
	}
	for _, want := range []string{"args(includeDeprecated: true)", "inputFields(includeDeprecated: true)"} {
		if !strings.Contains(*asked, want) {
			t.Errorf("introspection query lacks %s", want)
		}
	}
}

func TestSchemaCheck(t *testing.T) {
	cases := []struct {
		name string
		copy string
		url  func(t *testing.T) string
		want int
		out  string
	}{
		{"no drift", "type Query { jaar: Int }", func(t *testing.T) string { return source(t, 200, introspection) }, 0, "bri: no drift"},
		{"drift", "type Query { jaar: String }", func(t *testing.T) string { return source(t, 200, introspection) }, 1, "Query.jaar: returns String in copy, Int in source"},
		{"source answers an error", "type Query { jaar: Int }", func(t *testing.T) string { return source(t, 500, "boom") }, 2, "status 500"},
		{"introspection refused", "type Query { jaar: Int }", func(t *testing.T) string {
			return source(t, 200, `{"errors":[{"message":"introspection disabled"}]}`)
		}, 2, "carries errors"},
		{"source unreachable", "type Query { jaar: Int }", func(t *testing.T) string { return "http://127.0.0.1:1/graphql" }, 2, "introspection"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run([]string{"-catalog", catalog(t, tc.copy), "-service", "bri", "-url", tc.url(t)}, &stdout, &stderr)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d; stdout=%s stderr=%s", code, tc.want, &stdout, &stderr)
			}
			if out := stdout.String() + stderr.String(); !strings.Contains(out, tc.out) {
				t.Fatalf("output %q does not mention %q", out, tc.out)
			}
		})
	}
}

func TestSchemaCheckFromASavedIntrospection(t *testing.T) {
	file := filepath.Join(t.TempDir(), "introspection.json")
	if err := os.WriteFile(file, []byte(introspection), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-catalog", catalog(t, "type Query { jaar: Int }"), "-service", "bri", "-introspection", file}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d; stderr=%s", code, &stderr)
	}
}

func TestSchemaCheckNeedsExactlyOneSource(t *testing.T) {
	dir := catalog(t, "type Query { jaar: Int }")
	for _, args := range [][]string{
		{"-catalog", dir, "-service", "bri"},
		{"-catalog", dir, "-service", "bri", "-url", "http://x", "-introspection", "f"},
		{"-catalog", dir, "-service", "onbekend", "-url", "http://x"},
	} {
		if code := run(args, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
			t.Errorf("%v: exit = %d, want 2", args, code)
		}
	}
}
