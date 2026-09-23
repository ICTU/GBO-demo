package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := loadMockData("mockdata/owners.json")
	if err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("lvg-graphql-server-test")
	schema, err := buildSchema(tracer, store)
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	srv := httptest.NewServer(newMux(&schema, tracer, nil))
	t.Cleanup(srv.Close)
	return srv
}

func queryVbo(t *testing.T, srv *httptest.Server, bsn, vboID string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"query":     `query($bsn: BSN!, $vboId: String!) { vbo(bsn: $bsn, vboId: $vboId) { vboId } }`,
		"variables": map[string]string{"bsn": bsn, "vboId": vboID},
	})
	resp, err := http.Post(srv.URL+"/graphql", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Data   map[string]any `json:"data"`
		Errors []any          `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Errors) > 0 {
		t.Fatalf("graphql errors: %v", out.Errors)
	}
	return out.Data
}

func TestOwnerGetsTheSameVboIDBack(t *testing.T) {
	data := queryVbo(t, testServer(t), "123456789", "0632010000099412")
	vbo, ok := data["vbo"].(map[string]any)
	if !ok || vbo["vboId"] != "0632010000099412" {
		t.Fatalf("vbo = %v, want the requested VBO-id", data["vbo"])
	}
}

func TestNonOwnerGetsNull(t *testing.T) {
	// 987654321 owns 0632010000099413, not ...412: the answer must not
	// reveal which building they do own.
	data := queryVbo(t, testServer(t), "987654321", "0632010000099412")
	if data["vbo"] != nil {
		t.Fatalf("vbo = %v, want null", data["vbo"])
	}
}

func TestUnknownCitizenGetsNull(t *testing.T) {
	data := queryVbo(t, testServer(t), "555555555", "0632010000099412")
	if data["vbo"] != nil {
		t.Fatalf("vbo = %v, want null", data["vbo"])
	}
}

func TestActivityFollowsTheScope(t *testing.T) {
	l := &sourceLogbook{cfg: ldvQueryConfig{ScopeActivityBase: "https://lvg.example/activiteiten/"}}
	if got, want := l.activity("lvg:vbo:eigendom"), "https://lvg.example/activiteiten/lvg-vbo-eigendom/v1"; got != want {
		t.Errorf("activity = %q, want %q", got, want)
	}
	if got := l.activity(""); got != "" {
		t.Errorf("activity without scope = %q, want empty", got)
	}
}
