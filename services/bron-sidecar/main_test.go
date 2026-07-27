package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGrantPropsPreferAdditionalClaims(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"add": map[string]any{"subject_id_type": "pseudonym"},
		"prp": map[string]any{"subject_id_type": "direct"},
	})
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"

	properties := additionalClaimsFromAuth("Bearer " + token)
	if properties["subject_id_type"] != "pseudonym" {
		t.Fatalf("subject_id_type = %v, want pseudonym", properties["subject_id_type"])
	}
}

func TestGrantPropsSupportLegacyClaim(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"prp": map[string]any{"subject_id_type": "pseudonym"},
	})
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"

	properties := additionalClaimsFromAuth("Bearer " + token)
	if properties["subject_id_type"] != "pseudonym" {
		t.Fatalf("subject_id_type = %v, want pseudonym", properties["subject_id_type"])
	}
}

// Happy-path integration test for the sidecar's `direct` flow: no
// additional claim → default to `direct` → forward the GraphQL body verbatim
// to the upstream source (BSNk not touched). The stub upstream captures
// what it received so we can assert pass-through fidelity.
func TestForwardDirectPassThrough(t *testing.T) {
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"ingeschrevenPersoon":{"heeftBelastingjaarAangifte":[{"belastingjaar":2024}]}}}`))
	}))
	defer upstream.Close()

	cfg := config{
		Port:          "0",
		UpstreamURL:   upstream.URL,
		BSNkURL:       "http://unused.invalid",
		OwnPeerOIN:    "99999999900000000200",
		PseudonymVars: "bsn",
	}
	client := &http.Client{Timeout: 5 * time.Second}
	srv := httptest.NewServer(newMux(cfg, client))
	defer srv.Close()

	reqBody := `{"query":"query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { heeftBelastingjaarAangifte { belastingjaar } } }","variables":{"bsn":"123456789"}}`
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	// The `direct` path forwards the body verbatim — no rewrite.
	if receivedBody != reqBody {
		t.Fatalf("upstream body mismatch:\n got: %s\nwant: %s", receivedBody, reqBody)
	}

	var out struct {
		Data struct {
			IngeschrevenPersoon struct {
				HeeftBelastingjaarAangifte []struct {
					Belastingjaar int `json:"belastingjaar"`
				} `json:"heeftBelastingjaarAangifte"`
			} `json:"ingeschrevenPersoon"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data.IngeschrevenPersoon.HeeftBelastingjaarAangifte) != 1 || out.Data.IngeschrevenPersoon.HeeftBelastingjaarAangifte[0].Belastingjaar != 2024 {
		t.Fatalf("unexpected response shape: %+v", out)
	}
}

func TestHealth(t *testing.T) {
	cfg := config{UpstreamURL: "http://unused.invalid", BSNkURL: "http://unused.invalid", PseudonymVars: "bsn"}
	srv := httptest.NewServer(newMux(cfg, &http.Client{Timeout: time.Second}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", resp.StatusCode)
	}
}
