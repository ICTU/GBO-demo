package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Happy-path integration test: POST an issuance-server disclosure to the
// adapter's per-usecase endpoint. The FSC-Outway is stubbed with an
// httptest.Server returning a canned graphql-server response; the adapter
// should extract the BSN from the disclosure, call the (stubbed) outway,
// and return an IssuableDocument list in the bri-mock shape.
func TestAdapterEndToEnd(t *testing.T) {
	// Stub outway — returns a canned graphql response for the inkomens query.
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bri/graphql" {
			t.Errorf("outway path = %q, want /bri/graphql", r.URL.Path)
		}
		// Drain body so the adapter's json.Marshal side is exercised too.
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"inkomensgegevens": [{
					"belastingjaar": 2024,
					"verzamelinkomen": 42000.0,
					"peilDatum": "2025-05-01",
					"inkomenUitBox1": 40000.0,
					"inkomenUitBox2": 1000.0,
					"inkomenUitBox3": 1000.0,
					"grondslag": {"code": "A", "omschrijving": "aangifte"},
					"status": {"code": "V", "omschrijving": "vastgesteld"}
				}]
			}
		}`))
	}))
	defer outway.Close()

	cfg := config{
		Port:      "0",
		OutwayURL: outway.URL,
		IssuerOIN: "00000004000000004000",
	}
	catalog := &Catalog{
		Usecases: map[string]Usecase{
			"inkomensverklaring_2024": {
				AttestationType: "nl.gbo.belastingdienst.inkomensverklaring",
				Scope:           "bd:ib:2024",
				Belastingjaren:  []int{2024},
				OutwayPath:      "/bri/graphql",
			},
		},
	}
	srv := httptest.NewServer(newMux(cfg, catalog, http.DefaultClient))
	defer srv.Close()

	// Issuance-server request shape: one item, one attestation with a nested PID.
	body := []byte(`[{
		"id": "req-1",
		"attestations": [{
			"attestation_type": "urn:eudi:pid:nl:1",
			"attributes": {"urn:eudi:pid:nl:1": {"bsn": "123456789"}}
		}]
	}]`)
	resp, err := http.Post(srv.URL+"/inkomensverklaring_2024/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}

	var docs []attestation
	if err := json.NewDecoder(resp.Body).Decode(&docs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs len = %d, want 1", len(docs))
	}
	if docs[0].AttestationType != "nl.gbo.belastingdienst.inkomensverklaring" {
		t.Errorf("attestation_type = %q", docs[0].AttestationType)
	}
	// verzamelinkomen 42000.0 -> 4200000 eurocents. JSON round-trips numbers
	// as float64 into map[string]any, so compare via float.
	if got, want := docs[0].Attributes["verzamelinkomen"], float64(4200000); got != want {
		t.Errorf("verzamelinkomen = %v (%T), want %v", got, got, want)
	}
}
