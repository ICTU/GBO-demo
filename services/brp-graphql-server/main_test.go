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
	if err := loadMockData("mockdata/personen.json"); err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("brp-graphql-server-test")
	schema, err := buildSchema(tracer)
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	return httptest.NewServer(newMux(&schema, tracer))
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
