package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

// brpUsecase is the akte-van-overlijden catalog entry as a Go literal, so
// the tests do not depend on config/usecase_catalog.json.
var brpUsecase = Usecase{
	AttestationType: "nl.gbo.brp.akte-van-overlijden",
	Bron:            bronBRP,
	Scope:           "brp:akte:overlijden",
	OutwayPath:      "/brp/graphql",
}

// brpResponse is a canned BRP bron response for Frouke Jansen: one marriage,
// dissolved by the death of her partner, with `partners` symmetric (both
// spouses listed) exactly as the bronprofiel prescribes.
const brpResponse = `{
	"data": {
		"ingeschrevenPersoon": {
			"id": "018f2c4a-0001-7000-8000-000000000001",
			"bsn": "999991772",
			"geslachtsnaam": "Jansen",
			"voorvoegsel": null,
			"voornamen": "Frouke",
			"heeftHuwelijk": [{
				"soortVerbintenis": "Huwelijk",
				"datumVoltrekking": "2023-06-16",
				"plaatsVoltrekking": "Rotterdam",
				"landVoltrekking": "NL",
				"datumOntbinding": "2026-02-14",
				"redenOntbinding": "Overlijden",
				"partners": [
					{
						"id": "018f2c4a-0001-7000-8000-000000000001",
						"geslachtsnaam": "Jansen",
						"voornamen": "Frouke",
						"geboortedatum": "2000-03-24",
						"geslacht": "Vrouw",
						"datumOverlijden": null
					},
					{
						"id": "018f2c4a-0002-7000-8000-000000000002",
						"geslachtsnaam": "Vries",
						"voorvoegsel": "de",
						"voornamen": "Sander Willem",
						"geboortedatum": "1997-11-12",
						"geboorteplaats": "Rotterdam",
						"geboorteland": "NL",
						"geslacht": "Man",
						"datumOverlijden": "2026-02-14",
						"plaatsOverlijden": "Rotterdam",
						"landOverlijden": "NL",
						"heeftOuder": [
							{"geslachtsnaam": "Vries", "voorvoegsel": "de", "voornamen": "Hendrik Jan"},
							{"geslachtsnaam": "Bakker", "voornamen": "Annelies Maria"}
						]
					}
				]
			}]
		}
	}
}`

func brpAdapter(t *testing.T, bronResponse string) (*httptest.Server, *httptest.Server) {
	t.Helper()
	outway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/brp/graphql" {
			t.Errorf("outway path = %q, want /brp/graphql", r.URL.Path)
		}
		// The BRP flow must reach the PDP as its own flow, otherwise the
		// PDP resolves the query against the BD mirror schema.
		if got, want := r.Header.Get("X-GBO-Flow"), "eudi:attestation:brp"; got != want {
			t.Errorf("X-GBO-Flow = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("X-GBO-Scope"), "brp:akte:overlijden"; got != want {
			t.Errorf("X-GBO-Scope = %q, want %q", got, want)
		}
		raw, _ := io.ReadAll(r.Body)
		var req proxyRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("outway received unparseable body: %v", err)
		}
		if !strings.Contains(req.Query, "heeftHuwelijk") {
			t.Errorf("outway received a non-BRP query: %s", req.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bronResponse))
	}))
	cfg := config{Port: "0", OutwayURL: outway.URL, IssuerOIN: "00000004000000004000"}
	catalog := &Catalog{Usecases: map[string]Usecase{"akte_van_overlijden": brpUsecase}}
	srv := httptest.NewServer(newMux(cfg, catalog, http.DefaultClient))
	return outway, srv
}

func postDisclosure(t *testing.T, srv *httptest.Server, path, bsn string) *http.Response {
	t.Helper()
	body := []byte(`[{"id":"req-1","attestations":[{"attestation_type":"urn:eudi:pid:nl:1","attributes":{"urn:eudi:pid:nl:1":{"bsn":"` + bsn + `"}}}]}]`)
	resp, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

// Akte-van-overlijden happy path: the adapter must pick the deceased spouse
// out of the symmetric `partners` list and map the BRP fields onto the
// attestation attributes.
func TestAdapterAkteVanOverlijden(t *testing.T) {
	outway, srv := brpAdapter(t, brpResponse)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "999991772")
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
	if got, want := docs[0].AttestationType, "nl.gbo.brp.akte-van-overlijden"; got != want {
		t.Errorf("attestation_type = %q, want %q", got, want)
	}
	attrs := docs[0].Attributes
	want := map[string]any{
		"overledene_geslachtsnaam":  "Vries",
		"overledene_voorvoegsel":    "de",
		"overledene_voornamen":      "Sander Willem",
		"overledene_geboortedatum":  "1997-11-12",
		"overledene_geboorteplaats": "Rotterdam",
		"overledene_geslacht":       "Man",
		"datum_overlijden":          "2026-02-14",
		"plaats_overlijden":         "Rotterdam",
		"land_overlijden":           "NL",
		"soort_verbintenis":         "Huwelijk",
		"datum_voltrekking":         "2023-06-16",
		"datum_ontbinding":          "2026-02-14",
		"reden_ontbinding":          "Overlijden",
		"overledene_ouders":         "Hendrik Jan de Vries; Annelies Maria Bakker",
		"nabestaande_geslachtsnaam": "Jansen",
		"nabestaande_voornamen":     "Frouke",
	}
	for k, v := range want {
		if got := attrs[k]; got != v {
			t.Errorf("attribute %s = %v, want %v", k, got, v)
		}
	}
	// The nabestaande has no voorvoegsel — an akte must not assert an empty
	// claim for it.
	if _, ok := attrs["nabestaande_voorvoegsel"]; ok {
		t.Errorf("empty nabestaande_voorvoegsel should be omitted, got %v", attrs["nabestaande_voorvoegsel"])
	}
	if s, _ := attrs["verklaring_tekst"].(string); !strings.Contains(s, "Sander Willem de Vries") {
		t.Errorf("verklaring_tekst = %q, want the overledene's full name", s)
	}
}

// Married to a living partner: the bron answers, but there is nothing to
// certify — 404, not a 200 with an empty akte.
func TestAdapterAkteVanOverlijdenNoDeceasedPartner(t *testing.T) {
	response := `{"data":{"ingeschrevenPersoon":{
		"id":"p-1","bsn":"123456789","geslachtsnaam":"Bakker","voornamen":"Joost Pieter",
		"heeftHuwelijk":[{"soortVerbintenis":"Huwelijk","datumVoltrekking":"2016-08-20","partners":[
			{"id":"p-1","geslachtsnaam":"Bakker","voornamen":"Joost Pieter"},
			{"id":"p-2","geslachtsnaam":"Smit","voornamen":"Lisa"}
		]}]}}}`
	outway, srv := brpAdapter(t, response)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "123456789")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404; body = %s", resp.StatusCode, string(raw))
	}
}

// Unknown BSN: the bron returns null for ingeschrevenPersoon.
func TestAdapterAkteVanOverlijdenUnknownBSN(t *testing.T) {
	outway, srv := brpAdapter(t, `{"data":{"ingeschrevenPersoon":null}}`)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "000000000")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// Twice widowed: the most recent overlijden is the one being certified.
func TestSelectOverledenPartnerPicksMostRecent(t *testing.T) {
	persoon := brpPersoon{
		ID: "self",
		HeeftHuwelijk: []brpHuwelijk{
			{DatumVoltrekking: "1998-01-01", Partners: []brpPartner{
				{ID: "self"}, {ID: "a", Geslachtsnaam: "Eerste", DatumOverlijden: "2005-03-02"},
			}},
			{DatumVoltrekking: "2010-01-01", Partners: []brpPartner{
				{ID: "self"}, {ID: "b", Geslachtsnaam: "Tweede", DatumOverlijden: "2022-09-30"},
			}},
		},
	}
	huwelijk, overledene, ok := selectOverledenPartner(persoon)
	if !ok {
		t.Fatal("expected an overleden partner")
	}
	if overledene.Geslachtsnaam != "Tweede" {
		t.Errorf("overledene = %q, want Tweede (most recent)", overledene.Geslachtsnaam)
	}
	if huwelijk.DatumVoltrekking != "2010-01-01" {
		t.Errorf("huwelijk = %q, want the marriage of the most recent overledene", huwelijk.DatumVoltrekking)
	}
}

// The catalog shipped with the service must stay consistent with the code:
// every usecase names a bron the adapter knows, and its flow must match a
// flow the PDP has a mirror-schema for.
func TestShippedCatalogIsConsistent(t *testing.T) {
	catalog, err := loadCatalog("config/usecase_catalog.json")
	if err != nil {
		t.Fatalf("loadCatalog: %v", err)
	}
	for key, uc := range catalog.Usecases {
		switch uc.bron() {
		case bronBD:
			if uc.flow() != flowEudiBD {
				t.Errorf("usecase %s: flow = %q, want %q", key, uc.flow(), flowEudiBD)
			}
			if len(uc.Belastingjaren) == 0 {
				t.Errorf("usecase %s: BD usecase without belastingjaren", key)
			}
		case bronBRP:
			if uc.flow() != flowEudiBRP {
				t.Errorf("usecase %s: flow = %q, want %q", key, uc.flow(), flowEudiBRP)
			}
		default:
			t.Errorf("usecase %s: unknown bron %q", key, uc.bron())
		}
		if uc.Scope == "" || uc.OutwayPath == "" || uc.AttestationType == "" {
			t.Errorf("usecase %s: incomplete catalog entry %+v", key, uc)
		}
	}
}
