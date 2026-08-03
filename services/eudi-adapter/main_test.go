package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
		// The flow reaches the PDP as a signed additional-claim in the FSC
		// token, never as a header — a header would let the caller choose
		// the authorization regime it is judged under.
		if got := r.Header.Get("X-GBO-Flow"); got != "" {
			t.Errorf("X-GBO-Flow = %q, want no header", got)
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
		"overledene_geboorteland":   "NL",
		"overledene_geslacht":       "Man",
		"overledene_ouders":         "Hendrik Jan de Vries; Annelies Maria Bakker",
		"datum_overlijden":          "2026-02-14",
		"plaats_overlijden":         "Rotterdam",
		"land_overlijden":           "NL",
		"soort_verbintenis":         "Huwelijk",
		"echtgenoot_geslachtsnaam":  "Jansen",
		"echtgenoot_voornamen":      "Frouke",
	}
	for k, v := range want {
		if got := attrs[k]; got != v {
			t.Errorf("attribute %s = %v, want %v", k, got, v)
		}
	}
	// The echtgenoot has no voorvoegsel — an akte must not assert an empty
	// claim for it.
	if _, ok := attrs["echtgenoot_voorvoegsel"]; ok {
		t.Errorf("empty echtgenoot_voorvoegsel should be omitted, got %v", attrs["echtgenoot_voorvoegsel"])
	}
	if got, want := attrs["verklaring_tekst"], "Op 2026-02-14 is te Rotterdam overleden Sander Willem de Vries, geboren op 1997-11-12 te Rotterdam, gehuwd met Frouke Jansen."; got != want {
		t.Errorf("verklaring_tekst = %q, want %q", got, want)
	}
}

// The query fetches more than an akte carries: the huwelijk's voltrekking and
// ontbinding are needed to select the right marriage and to establish that it
// ended in death, but they belong on the huwelijksakte. The BSN and the
// persoonslijst-ids are not on an akte either. None of them may leak into the
// credential.
func TestAkteVanOverlijdenOmitsNonAkteFields(t *testing.T) {
	outway, srv := brpAdapter(t, brpResponse)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "999991772")
	defer resp.Body.Close()
	var docs []attestation
	if err := json.NewDecoder(resp.Body).Decode(&docs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, forbidden := range []string{
		"datum_voltrekking", "plaats_voltrekking", "land_voltrekking",
		"datum_ontbinding", "reden_ontbinding",
		"bsn", "id", "nabestaande_geslachtsnaam",
	} {
		if v, ok := docs[0].Attributes[forbidden]; ok {
			t.Errorf("attribute %q must not be on an akte van overlijden, got %v", forbidden, v)
		}
	}

	// Every attribute the adapter emits must be declared in the credential
	// metadata, otherwise the issuance-server rejects the document.
	raw, err := os.ReadFile("../eudi-issuance-server/config/akte_van_overlijden_metadata.json.example")
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var meta struct {
		VCT    string `json:"vct"`
		Claims []struct {
			Path []string `json:"path"`
		} `json:"claims"`
		Schema struct {
			Properties map[string]any `json:"properties"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse metadata: %v", err)
	}
	if meta.VCT != docs[0].AttestationType {
		t.Errorf("metadata vct = %q, adapter emits %q", meta.VCT, docs[0].AttestationType)
	}
	declared := map[string]bool{}
	for _, c := range meta.Claims {
		declared[c.Path[0]] = true
	}
	for name := range docs[0].Attributes {
		if !declared[name] {
			t.Errorf("attribute %q is not declared in the credential metadata", name)
		}
		if _, ok := meta.Schema.Properties[name]; !ok {
			t.Errorf("attribute %q is not in the credential JSON-schema", name)
		}
	}
}

// A geregistreerd partnerschap is worded differently on the akte than a
// huwelijk.
func TestAkteVerklaringGeregistreerdPartnerschap(t *testing.T) {
	persoon := brpPersoon{Geslachtsnaam: "Jansen", Voornamen: "Frouke"}
	huwelijk := brpHuwelijk{SoortVerbintenis: "GeregistreerdPartnerschap"}
	overledene := brpPartner{
		Geslachtsnaam: "Vries", Voorvoegsel: "de", Voornamen: "Sander Willem",
		DatumOverlijden: "2026-02-14", PlaatsOverlijden: "Rotterdam",
	}
	got := akteVerklaring(persoon, huwelijk, overledene)
	want := "Op 2026-02-14 is te Rotterdam overleden Sander Willem de Vries, geregistreerd partner van Frouke Jansen."
	if got != want {
		t.Errorf("verklaring = %q, want %q", got, want)
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
			{DatumVoltrekking: "1998-01-01", DatumOntbinding: "2005-03-02", RedenOntbinding: "Overlijden",
				Partners: []brpPartner{
					{ID: "self"}, {ID: "a", Geslachtsnaam: "Eerste", DatumOverlijden: "2005-03-02"},
				}},
			{DatumVoltrekking: "2010-01-01", DatumOntbinding: "2022-09-30", RedenOntbinding: "Overlijden",
				Partners: []brpPartner{
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

// Divorced first, ex-partner died years later: the BRP still carries that
// marriage and that datumOverlijden, but the echtscheiding — not this death —
// ended the verbintenis, so there is no akte to hand the ex-spouse.
func TestSelectOverledenPartnerIgnoresEchtscheiding(t *testing.T) {
	persoon := brpPersoon{
		ID: "self",
		HeeftHuwelijk: []brpHuwelijk{
			{DatumVoltrekking: "1998-01-01", DatumOntbinding: "2004-06-11", RedenOntbinding: "Echtscheiding",
				Partners: []brpPartner{
					{ID: "self"}, {ID: "a", Geslachtsnaam: "Ex", DatumOverlijden: "2021-07-19"},
				}},
		},
	}
	if _, overledene, ok := selectOverledenPartner(persoon); ok {
		t.Errorf("selected %q from a marriage ended by echtscheiding, want no match", overledene.Geslachtsnaam)
	}
}

// Ontbonden door overlijden, but on a different day than the partner died:
// the two do not describe the same event, so the akte is not issued.
func TestSelectOverledenPartnerRejectsMismatchedDates(t *testing.T) {
	persoon := brpPersoon{
		ID: "self",
		HeeftHuwelijk: []brpHuwelijk{
			{DatumVoltrekking: "1998-01-01", DatumOntbinding: "2005-03-02", RedenOntbinding: "Overlijden",
				Partners: []brpPartner{
					{ID: "self"}, {ID: "a", Geslachtsnaam: "Eerste", DatumOverlijden: "2011-08-23"},
				}},
		},
	}
	if _, overledene, ok := selectOverledenPartner(persoon); ok {
		t.Errorf("selected %q on mismatched ontbinding/overlijden dates, want no match", overledene.Geslachtsnaam)
	}
}

// A DatumIncompleet the bron only knows to the year cannot be compared
// day-for-day. Refusing the akte over the bron's date precision would be
// worse than accepting it, so the partial is let through.
func TestSelectOverledenPartnerAcceptsPartialDates(t *testing.T) {
	persoon := brpPersoon{
		ID: "self",
		HeeftHuwelijk: []brpHuwelijk{
			{DatumVoltrekking: "1998-01-01", DatumOntbinding: "2005", RedenOntbinding: "Overlijden",
				Partners: []brpPartner{
					{ID: "self"}, {ID: "a", Geslachtsnaam: "Eerste", DatumOverlijden: "2005-03-02"},
				}},
		},
	}
	_, overledene, ok := selectOverledenPartner(persoon)
	if !ok {
		t.Fatal("expected an overleden partner on a partial datumOntbinding")
	}
	if overledene.Geslachtsnaam != "Eerste" {
		t.Errorf("overledene = %q, want Eerste", overledene.Geslachtsnaam)
	}
}

// Widowed once and divorced once, the ex dying later: the akte must be about
// the partner whose death actually ended a marriage — end to end, through the
// adapter, not just through the selection helper.
func TestAdapterAkteVanOverlijdenSkipsLaterDeathOfExPartner(t *testing.T) {
	response := `{"data":{"ingeschrevenPersoon":{
		"id":"p-1","bsn":"999991772","geslachtsnaam":"Jansen","voornamen":"Frouke",
		"heeftHuwelijk":[
			{"soortVerbintenis":"Huwelijk","datumVoltrekking":"1998-01-01",
			 "datumOntbinding":"2004-06-11","redenOntbinding":"Echtscheiding","partners":[
				{"id":"p-1","geslachtsnaam":"Jansen","voornamen":"Frouke"},
				{"id":"p-9","geslachtsnaam":"Ex","voornamen":"Oude","datumOverlijden":"2026-05-01"}
			]},
			{"soortVerbintenis":"Huwelijk","datumVoltrekking":"2010-01-01",
			 "datumOntbinding":"2022-09-30","redenOntbinding":"Overlijden","partners":[
				{"id":"p-1","geslachtsnaam":"Jansen","voornamen":"Frouke"},
				{"id":"p-2","geslachtsnaam":"Vries","voorvoegsel":"de","voornamen":"Sander Willem","datumOverlijden":"2022-09-30"}
			]}
		]}}}`
	outway, srv := brpAdapter(t, response)
	defer outway.Close()
	defer srv.Close()

	resp := postDisclosure(t, srv, "/akte_van_overlijden/", "999991772")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, string(raw))
	}
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	// The ex-partner died most recently, so a naive "latest datumOverlijden"
	// selection would certify him instead.
	if strings.Contains(body, "2026-05-01") || strings.Contains(body, `"Oude"`) {
		t.Errorf("akte certifies the ex-partner's later death; body = %s", body)
	}
	if !strings.Contains(body, "2022-09-30") {
		t.Errorf("akte does not certify the death that ended the marriage; body = %s", body)
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
