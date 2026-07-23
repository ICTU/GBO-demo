package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gqlparser "github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestTokenPropertiesPreferAdditionalClaims(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"add": map[string]any{"flow": "dvtp:query"},
		"prp": map[string]any{"flow": "legacy"},
	})
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"

	properties := tokenAdditionalClaimsFromHeaders(map[string]string{
		"Fsc-Authorization": "Bearer " + token,
	})
	if properties["flow"] != "dvtp:query" {
		t.Fatalf("flow = %v, want dvtp:query", properties["flow"])
	}
}

func TestTokenPropertiesSupportLegacyClaim(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"prp": map[string]any{"flow": "dvtp:query"},
	})
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"

	properties := tokenAdditionalClaimsFromHeaders(map[string]string{
		"Fsc-Authorization": "Bearer " + token,
	})
	if properties["flow"] != "dvtp:query" {
		t.Fatalf("flow = %v, want dvtp:query", properties["flow"])
	}
}

// Minimal in-memory schema used by the /evaluation handler when enriching
// the OPA input. Structurally similar to policies/dvtp/schemas/*.graphql
// but stripped down to keep the test hermetic.
const testDvtpSDL = `
type Query {
  inkomensgegevens(input: InkomensgegevensInput!): [InkomensgegevensPerJaar!]!
}
input InkomensgegevensInput {
  consentId: ID!
  belastingjaren: [Int!]
}
type InkomensgegevensPerJaar {
  belastingjaar: Int!
  verzamelinkomen: Int
}
`

func loadTestSchemas(t *testing.T) map[string]*ast.Schema {
	t.Helper()
	s, err := gqlparser.LoadSchema(&ast.Source{Name: "test-dvtp.graphql", Input: testDvtpSDL})
	if err != nil {
		t.Fatalf("load test schema: %v", err)
	}
	return map[string]*ast.Schema{
		"dvtp:query":       s,
		"eudi:attestation": s,
	}
}

// TestHealth verifies the health-endpoint responds without any external
// dependencies.
func TestHealth(t *testing.T) {
	cfg := config{OPAURL: "http://unused"}
	srv := httptest.NewServer(newMux(cfg, http.DefaultClient, loadTestSchemas(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
}

// TestFSCAuthZenHappyPath stubs OPA + consent-register and POSTs a valid
// AuthZen envelope to /evaluation. Verifies the PDP:
//   - parses the envelope
//   - dispatches to enrichInput
//   - forwards the enriched input to OPA
//   - translates OPA's {result:{decision,context}} back to the AuthZen
//     wire-shape {decision, context}
func TestFSCAuthZenHappyPath(t *testing.T) {
	// Stub consent-register: return one ACTIVE consent for the by-PI lookup
	// so the DvTP-flow enrichment succeeds.
	consentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/consents") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"status":"ACTIVE","scopes":["inkomen:read"],"valid_until":"2099-01-01T00:00:00Z","pi":"PI-abc123"}]`))
	}))
	defer consentSrv.Close()

	// Stub OPA: assert we received a POST to /v1/data/dvtp/authz with
	// enriched input, then respond with an allow-decision.
	var opaSawEnriched bool
	opaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/data/dvtp/authz" {
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var env struct {
			Input map[string]json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(body, &env)
		// enrichInput MUST populate resolved and pip on the OPA input;
		// this is the load-bearing contract between PDP and OPA.
		if _, hasResolved := env.Input["resolved"]; hasResolved {
			if _, hasPip := env.Input["pip"]; hasPip {
				opaSawEnriched = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"decision":true,"context":{"reason_admin":{"code":"OK"}}}}`))
	}))
	defer opaSrv.Close()

	cfg := config{
		OPAURL:     opaSrv.URL,
		ConsentURL: consentSrv.URL,
	}
	client := &http.Client{Timeout: 5 * time.Second}
	srv := httptest.NewServer(newMux(cfg, client, loadTestSchemas(t)))
	defer srv.Close()

	envelope := map[string]any{
		"subject": map[string]any{
			"id":         "peer-oin-123",
			"properties": map[string]any{"outway_peer_id": "peer-oin-123"},
		},
		"action": map[string]any{
			"name": "dvtp:query",
			"properties": map[string]any{
				"body": `{"query":"query($bsn: ID!){ inkomensgegevens(input:{consentId:$bsn}){ belastingjaar verzamelinkomen } }","variables":{"bsn":"PI-abc123"}}`,
			},
		},
		"context": map[string]any{
			"headers": map[string]string{
				"X-Gbo-Scope": "inkomen:read",
				"X-Gbo-Flow":  "dvtp:query",
			},
		},
	}
	body, _ := json.Marshal(envelope)
	resp, err := http.Post(srv.URL+"/evaluation", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post /evaluation: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("evaluation status = %d, want 200; body=%s", resp.StatusCode, string(respBody))
	}
	var authzen struct {
		Decision bool           `json:"decision"`
		Context  map[string]any `json:"context"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&authzen); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !authzen.Decision {
		t.Fatalf("decision = false, want true; ctx=%+v", authzen.Context)
	}
	if !opaSawEnriched {
		t.Fatalf("OPA did not receive enriched input (resolved + pip missing)")
	}
}
