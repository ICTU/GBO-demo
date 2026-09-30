package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Happy-path integration test: pseudonymize a demo BSN, then transform
// the returned PI back to BSN. Verifies the composition root wires the two
// handlers of the first interface through the shared store correctly.
func TestPseudonymizeThenTransform(t *testing.T) {
	mux, err := newMux(config{})
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	pseudReq := bytes.NewBufferString(`{"bsn":"987654321","recipient_oin":"99999999900000000200"}`)
	pseudResp, err := http.Post(srv.URL+"/pseudonymize", "application/json", pseudReq)
	if err != nil {
		t.Fatalf("pseudonymize: %v", err)
	}
	defer pseudResp.Body.Close()
	if pseudResp.StatusCode != http.StatusOK {
		t.Fatalf("pseudonymize status = %d, want 200", pseudResp.StatusCode)
	}
	var pseud struct {
		Pseudonym string `json:"pseudonym"`
		PI        string `json:"pi"`
	}
	if err := json.NewDecoder(pseudResp.Body).Decode(&pseud); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pseud.PI == "" || pseud.Pseudonym == "" {
		t.Fatalf("empty PI or pseudonym: %+v", pseud)
	}

	trReq := bytes.NewBufferString(`{"pi":"` + pseud.PI + `","recipient_oin":"99999999900000000200"}`)
	trResp, err := http.Post(srv.URL+"/transform", "application/json", trReq)
	if err != nil {
		t.Fatalf("transform: %v", err)
	}
	defer trResp.Body.Close()
	if trResp.StatusCode != http.StatusOK {
		t.Fatalf("transform status = %d, want 200", trResp.StatusCode)
	}
	var tr struct {
		BSN string `json:"bsn"`
	}
	if err := json.NewDecoder(trResp.Body).Decode(&tr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if tr.BSN != "987654321" {
		t.Fatalf("BSN roundtrip mismatch: got %q, want %q", tr.BSN, "987654321")
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("BSN_AUTHORISED_OINS", " 99999999900000000200, 99999999900000000210 ,")
	t.Setenv("RANDOMIZE_VALUES", "true")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.BSNAuthorisedOINs) != 2 || cfg.BSNAuthorisedOINs[1] != "99999999900000000210" || !cfg.Randomize {
		t.Errorf("cfg = %+v", cfg)
	}

	t.Setenv("RANDOMIZE_VALUES", "sometimes")
	if _, err := loadConfig(); err == nil {
		t.Error("RANDOMIZE_VALUES=sometimes was accepted")
	}
}

// A misconfigured list stops the service at start instead of silently
// leaving a party without its BSN.
func TestNewMuxRejectsAMalformedOIN(t *testing.T) {
	if _, err := newMux(config{BSNAuthorisedOINs: []string{"bd-mock"}}); err == nil {
		t.Error("newMux accepted a name as an OIN")
	}
}
