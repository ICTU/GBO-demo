package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Installatie Register's instance asks LVG whether the citizen of the
// consent owns one verblijfsobject: the PI and the VBO-id travel as variables,
// under the LVG scope, and LVG's answer comes back as data.
func TestOwnershipQueryReachesLVG(t *testing.T) {
	var outwayPath, scope string
	var sent struct {
		Query     string            `json:"query"`
		Variables map[string]string `json:"variables"`
	}
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outwayPath = r.URL.Path
		scope = r.Header.Get("X-GBO-Scope")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		_, _ = w.Write([]byte(`{"data":{"vbo":{"vboId":"0632010000099412"}}}`))
	}))
	defer outway.Close()

	srv := httptest.NewServer(newMux(config{
		OutwayURL:  outway.URL,
		OutwayPath: "/lvg/graphql",
		Kind:       queryKinds["lvg"],
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/dvtp/query", "application/json",
		strings.NewReader(testQueryBody("c-ir", []string{"lvg:vbo:eigendom"}, map[string]any{"vbo_id": "0632010000099412"})))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var out queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Allowed || !strings.Contains(string(out.Data), "0632010000099412") {
		t.Fatalf("response = %+v, want allowed with LVG's answer", out)
	}
	if outwayPath != "/lvg/graphql" {
		t.Errorf("outway path = %q, want /lvg/graphql", outwayPath)
	}
	if scope != "lvg:vbo:eigendom" {
		t.Errorf("X-GBO-Scope = %q, want the LVG scope by default", scope)
	}
	if !strings.Contains(sent.Query, "vbo(bsn: $bsn, vboId: $vboId)") {
		t.Errorf("query = %q, want the ownership check", sent.Query)
	}
	if sent.Variables["bsn"] != "PI-abc123" || sent.Variables["vboId"] != "0632010000099412" {
		t.Errorf("variables = %v, want the consent's PI and the requested VBO-id", sent.Variables)
	}
}

// Without a VBO-id there is no question to ask; the request never reaches FSC.
func TestOwnershipQueryRequiresAVboID(t *testing.T) {
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the outway was called without a VBO-id")
	}))
	defer outway.Close()

	srv := httptest.NewServer(newMux(config{OutwayURL: outway.URL, OutwayPath: "/lvg/graphql", Kind: queryKinds["lvg"]}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/dvtp/query", "application/json",
		strings.NewReader(testQueryBody("c-ir", []string{"lvg:vbo:eigendom"}, nil)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestUnknownQueryKindIsRefused(t *testing.T) {
	if _, err := lookupQueryKind("brp"); err == nil {
		t.Fatal("lookupQueryKind accepted an unknown kind")
	}
}
