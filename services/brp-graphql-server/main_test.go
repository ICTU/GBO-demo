package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
)

// overlijdenPartnerQuery is the demo use-case query, verbatim from the
// bronprofiel BRP: walk from the surviving partner to the marriage and its
// partners. `ingeschrevenPersoon` returns the IngeschrevenPersoon interface,
// so no inline fragment is needed for these fields.
const overlijdenPartnerQuery = `query OverlijdenPartner($bsn: BSN!) {
  ingeschrevenPersoon(bsn: $bsn) {
    id bsn geslachtsnaam voorvoegsel voornamen
    heeftHuwelijk {
      soortVerbintenis datumVoltrekking plaatsVoltrekking landVoltrekking
      datumOntbinding redenOntbinding
      partners {
        id geslachtsnaam voorvoegsel voornamen
        geboortedatum geboorteplaats geboorteland geslacht
        datumOverlijden plaatsOverlijden landOverlijden
        heeftOuder { geslachtsnaam voorvoegsel voornamen }
      }
    }
  }
}`

type overlijdenResult struct {
	Data struct {
		IngeschrevenPersoon *struct {
			ID            string  `json:"id"`
			BSN           string  `json:"bsn"`
			Geslachtsnaam string  `json:"geslachtsnaam"`
			Voornamen     *string `json:"voornamen"`
			HeeftHuwelijk []struct {
				SoortVerbintenis string  `json:"soortVerbintenis"`
				DatumVoltrekking *string `json:"datumVoltrekking"`
				DatumOntbinding  *string `json:"datumOntbinding"`
				RedenOntbinding  *string `json:"redenOntbinding"`
				Partners         []struct {
					ID               string  `json:"id"`
					Geslachtsnaam    string  `json:"geslachtsnaam"`
					Voorvoegsel      *string `json:"voorvoegsel"`
					Voornamen        *string `json:"voornamen"`
					Geslacht         *string `json:"geslacht"`
					DatumOverlijden  *string `json:"datumOverlijden"`
					PlaatsOverlijden *string `json:"plaatsOverlijden"`
					HeeftOuder       []struct {
						Geslachtsnaam string  `json:"geslachtsnaam"`
						Voorvoegsel   *string `json:"voorvoegsel"`
						Voornamen     *string `json:"voornamen"`
					} `json:"heeftOuder"`
				} `json:"partners"`
			} `json:"heeftHuwelijk"`
		} `json:"ingeschrevenPersoon"`
	} `json:"data"`
	Errors []any `json:"errors"`
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store, err := loadMockData("mockdata/personen.json")
	if err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("brp-graphql-server-test")
	schema, err := buildSchema(tracer, store)
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	return httptest.NewServer(newMux(&schema, tracer, nil))
}

func post(t *testing.T, srv *httptest.Server, query, bsn string) overlijdenResult {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"bsn": bsn},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, b)
	}
	var out overlijdenResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Errors) > 0 {
		t.Fatalf("graphql errors: %+v", out.Errors)
	}
	return out
}

// The akte-van-overlijden happy path: Frouke Jansen (the BSN the wallet demo
// discloses) has exactly one marriage, dissolved by the death of her partner.
func TestOverlijdenPartner(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	out := post(t, srv, overlijdenPartnerQuery, "999991772")
	persoon := out.Data.IngeschrevenPersoon
	if persoon == nil {
		t.Fatalf("no persoon for BSN 999991772: %+v", out)
	}
	if persoon.Geslachtsnaam != "Jansen" || persoon.Voornamen == nil || *persoon.Voornamen != "Frouke" {
		t.Fatalf("unexpected persoon %+v", persoon)
	}
	if len(persoon.HeeftHuwelijk) != 1 {
		t.Fatalf("expected exactly 1 huwelijk, got %d", len(persoon.HeeftHuwelijk))
	}
	huwelijk := persoon.HeeftHuwelijk[0]
	if huwelijk.SoortVerbintenis != "Huwelijk" {
		t.Errorf("soortVerbintenis = %q, want %q", huwelijk.SoortVerbintenis, "Huwelijk")
	}
	if huwelijk.RedenOntbinding == nil || *huwelijk.RedenOntbinding != "Overlijden" {
		t.Errorf("redenOntbinding = %v, want Overlijden", huwelijk.RedenOntbinding)
	}
	// partners is symmetric — both spouses are listed; exactly one of them
	// has a datumOverlijden.
	if len(huwelijk.Partners) != 2 {
		t.Fatalf("expected 2 partners (symmetric), got %d", len(huwelijk.Partners))
	}
	var overleden int
	for _, p := range huwelijk.Partners {
		if p.DatumOverlijden == nil {
			continue
		}
		overleden++
		if p.Geslachtsnaam != "Vries" || p.Voorvoegsel == nil || *p.Voorvoegsel != "de" {
			t.Errorf("unexpected overleden partner %+v", p)
		}
		if *p.DatumOverlijden != "2026-02-14" || p.PlaatsOverlijden == nil || *p.PlaatsOverlijden != "Rotterdam" {
			t.Errorf("unexpected overlijden data %+v", p)
		}
		if len(p.HeeftOuder) != 2 {
			t.Errorf("expected 2 ouders for the overledene, got %d", len(p.HeeftOuder))
		}
		if p.Geslacht == nil || *p.Geslacht != "Man" {
			t.Errorf("geslacht = %v, want Man (enum serialisation)", p.Geslacht)
		}
	}
	if overleden != 1 {
		t.Fatalf("expected exactly 1 overleden partner, got %d", overleden)
	}
}

// Only one person in the mock satisfies the akte-van-overlijden shape: the
// others are married to a living partner, unmarried, or divorced.
func TestOnlyOnePersoonHasOverledenPartner(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	for _, bsn := range []string{"123456789", "987654321", "555555555"} {
		out := post(t, srv, overlijdenPartnerQuery, bsn)
		persoon := out.Data.IngeschrevenPersoon
		if persoon == nil {
			t.Fatalf("no persoon for BSN %s", bsn)
		}
		for _, h := range persoon.HeeftHuwelijk {
			for _, p := range h.Partners {
				if p.DatumOverlijden != nil {
					t.Errorf("BSN %s unexpectedly has an overleden partner: %+v", bsn, p)
				}
			}
		}
	}
}

func TestAkteRejectsDeathThatDidNotEndMarriage(t *testing.T) {
	persoon := testWidow("2024-01-02", "2024-01-03")
	if _, ok := akteVanOverlijden(persoon); ok {
		t.Fatal("akteVanOverlijden accepted a partner death on a different date than the marriage dissolution")
	}
}

func TestAkteAllowsIncompleteDates(t *testing.T) {
	persoon := testWidow("2024-01", "2024-01-03")
	if _, ok := akteVanOverlijden(persoon); !ok {
		t.Fatal("akteVanOverlijden rejected a BRP DatumIncompleet that cannot be compared day-for-day")
	}
}

func TestAkteRepresentsMissingOptionalFieldsAsNull(t *testing.T) {
	persoon := testWidow("2024-01-03", "2024-01-03")
	persoon.Geslachtsnaam = ""
	akte, ok := akteVanOverlijden(persoon)
	if !ok {
		t.Fatal("akteVanOverlijden returned no result")
	}
	for _, claim := range []string{
		"overledene_voorvoegsel", "overledene_geboortedatum", "overledene_geboorteplaats",
		"overledene_geboorteland", "overledene_geslacht", "overledene_ouders",
		"plaats_overlijden", "land_overlijden", "echtgenoot_geslachtsnaam", "echtgenoot_voorvoegsel", "echtgenoot_voornamen",
	} {
		if value := akte[claim]; value != nil {
			t.Errorf("%s = %#v, want nil", claim, value)
		}
	}
}

func testWidow(datumOntbinding, datumOverlijden string) Persoon {
	partnerID := "partner"
	requesterID := "requester"
	partnerName := "Partner"
	return Persoon{
		ID: requesterID, Geslachtsnaam: "Requester",
		HeeftHuwelijk: []Huwelijk{{
			SoortVerbintenis: "Huwelijk", DatumOntbinding: &datumOntbinding,
			RedenOntbinding: stringPointer("Overlijden"),
			Partners: []NatuurlijkPersoon{
				{ID: requesterID, Geslachtsnaam: "Requester"},
				{ID: partnerID, Geslachtsnaam: "Partner", Voornamen: &partnerName, DatumOverlijden: &datumOverlijden},
			},
		}},
	}
}

func stringPointer(value string) *string { return &value }

// The concrete types behind the IngeschrevenPersoon interface must be
// reachable through inline fragments — Ingezetene carries a binnenlands
// verblijfadres, NietIngezetene a buitenlands one.
func TestConcreteTypesViaInlineFragment(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	query := `query($bsn: BSN!) {
	  ingeschrevenPersoon(bsn: $bsn) {
	    bsn
	    heeftNationaliteit { nationaliteit }
	    ... on Ingezetene { indicatieGeheim woontOp { straatnaam huisnummer woonplaatsnaam } }
	    ... on NietIngezetene { landVanVerblijf woontOp { adresregel1 land } }
	  }
	}`

	body, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"bsn": "999991772"}})
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var out struct {
		Data struct {
			IngeschrevenPersoon map[string]any `json:"ingeschrevenPersoon"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Errors) > 0 {
		t.Fatalf("graphql errors: %+v", out.Errors)
	}
	adres, ok := out.Data.IngeschrevenPersoon["woontOp"].(map[string]any)
	if !ok {
		t.Fatalf("expected Ingezetene.woontOp for BSN 999991772, got %+v", out.Data.IngeschrevenPersoon)
	}
	if got, want := adres["woonplaatsnaam"], "Rotterdam"; got != want {
		t.Errorf("woonplaatsnaam = %v, want %v", got, want)
	}
	// Two nationalities (NL + BE), as on the PID-preprod persona.
	nats, _ := out.Data.IngeschrevenPersoon["heeftNationaliteit"].([]any)
	if len(nats) != 2 {
		t.Errorf("expected 2 nationaliteiten, got %d", len(nats))
	}
}

func TestUnknownBSNResolvesToNull(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	out := post(t, srv, overlijdenPartnerQuery, "000000000")
	if out.Data.IngeschrevenPersoon != nil {
		t.Fatalf("expected null for unknown BSN, got %+v", out.Data.IngeschrevenPersoon)
	}
}

// This server serves GraphQL and nothing else: the playground lives in the
// developer-portal and reaches this endpoint over its own proxy. What that
// page does need is introspection, which graphql-go answers unconditionally
// — the Schema tab is built from it.
func TestGraphQLServesNoUIButIntrospects(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/graphql", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), "<html") {
		t.Fatalf("browser GET /graphql served HTML; this server has no UI:\n%s", string(body))
	}

	introspection := `{"query":"{ __schema { queryType { name } } }"}`
	ir, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(introspection))
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	defer ir.Body.Close()
	var out struct {
		Data struct {
			Schema struct {
				QueryType struct {
					Name string `json:"name"`
				} `json:"queryType"`
			} `json:"__schema"`
		} `json:"data"`
	}
	if err := json.NewDecoder(ir.Body).Decode(&out); err != nil {
		t.Fatalf("decode introspection: %v", err)
	}
	if out.Data.Schema.QueryType.Name == "" {
		t.Fatal("introspection returned no queryType; the portal's Schema tab needs it")
	}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
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
