package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Happy-path integration test for the sidecar's `direct` flow: no
// grant-property → default to `direct` → forward the GraphQL body verbatim
// to the upstream source (BSNk not touched). The stub upstream captures
// what it received so we can assert pass-through fidelity.
func TestForwardDirectPassThrough(t *testing.T) {
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"inkomensgegevens":[{"belastingjaar":2024}]}}`))
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

	reqBody := `{"query":"query($bsn: String!) { inkomensgegevens(input:{burgerservicenummer:$bsn}) { belastingjaar } }","variables":{"bsn":"123456789"}}`
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
			Inkomensgegevens []struct {
				Belastingjaar int `json:"belastingjaar"`
			} `json:"inkomensgegevens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data.Inkomensgegevens) != 1 || out.Data.Inkomensgegevens[0].Belastingjaar != 2024 {
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
