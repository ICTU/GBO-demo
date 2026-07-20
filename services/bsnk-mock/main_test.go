package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Happy-path integration test: pseudonymize a demo BSN, then transform
// the returned PI back to BSN. Verifies the mux wires the two handlers
// through the shared store correctly.
func TestPseudonymizeThenTransform(t *testing.T) {
	srv := httptest.NewServer(newMux(NewStore()))
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
