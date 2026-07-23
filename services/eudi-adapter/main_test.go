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
// httptest.Server returning a canned graphql-server response (BD-schema);
// the adapter should extract the BSN from the disclosure, call the
// (stubbed) outway, select the usecase's belastingjaar, and return an
// IssuableDocument list in the bri-mock shape.
func TestAdapterEndToEnd(t *testing.T) {
	// Stub outway — returns a canned BD-graphql response with two aangiften.
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bri/graphql" {
			t.Errorf("outway path = %q, want /bri/graphql", r.URL.Path)
		}
		// Drain body so the adapter's json.Marshal side is exercised too.
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"ingeschrevenPersoon": {
					"heeftBelastingjaarAangifte": [
						{
							"belastingjaar": 2023,
							"status": "Definitief vastgesteld",
							"indieningsdatum": "2024-04-01",
							"verzamelinkomen": {"waarde": 40000.0, "valuta": "EUR"}
						},
						{
							"belastingjaar": 2024,
							"status": "Voorlopig vastgesteld",
							"indieningsdatum": "2025-05-01",
							"verzamelinkomen": {"waarde": 42000.0, "valuta": "EUR"},
							"box1Inkomen": {"waarde": 40000.0, "valuta": "EUR"},
							"box2Inkomen": {"waarde": 1000.0, "valuta": "EUR"},
							"box3Inkomen": {"waarde": 1000.0, "valuta": "EUR"}
						}
					]
				}
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
	// Usecase belastingjaren [2024] must select the 2024 aangifte, not the
	// first (2023) one in the bron response.
	if got, want := docs[0].Attributes["belastingjaar"], float64(2024); got != want {
		t.Errorf("belastingjaar = %v, want %v", got, want)
	}
	// verzamelinkomen 42000.0 -> 42000 whole euros. JSON round-trips numbers
	// as float64 into map[string]any, so compare via float.
	if got, want := docs[0].Attributes["verzamelinkomen"], float64(42000); got != want {
		t.Errorf("verzamelinkomen = %v (%T), want %v", got, got, want)
	}
	if got, want := docs[0].Attributes["aangifte_status"], "Voorlopig vastgesteld"; got != want {
		t.Errorf("aangifte_status = %v, want %v", got, want)
	}
	if got, want := docs[0].Attributes["indieningsdatum"], "2025-05-01"; got != want {
		t.Errorf("indieningsdatum = %v, want %v", got, want)
	}
}
