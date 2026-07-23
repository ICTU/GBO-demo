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

// Happy-path integration test: load the demo mock data, build the schema,
// spin up the mux behind an httptest.Server, and issue a GraphQL query for
// the pre-baked BSN 123456789. Verifies wiring: /graphql handler → schema
// resolver → mock store.
func TestGraphQLHappyPath(t *testing.T) {
	if err := loadMockData("mockdata/citizens.json"); err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("graphql-server-test")
	schema, err := buildSchema(tracer)
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	srv := httptest.NewServer(newMux(&schema, tracer))
	defer srv.Close()

	body := `{"query":"query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { bsn heeftBelastingjaarAangifte { belastingjaar status indieningsdatum ... on AangifteIH { verzamelinkomen { waarde valuta } } } } }","variables":{"bsn":"123456789"}}`
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, b)
	}

	var out struct {
		Data struct {
			IngeschrevenPersoon *struct {
				BSN                        string `json:"bsn"`
				HeeftBelastingjaarAangifte []struct {
					Belastingjaar   int     `json:"belastingjaar"`
					Indieningsdatum *string `json:"indieningsdatum"`
					Verzamelinkomen *struct {
						Waarde float64 `json:"waarde"`
						Valuta *string `json:"valuta"`
					} `json:"verzamelinkomen"`
				} `json:"heeftBelastingjaarAangifte"`
			} `json:"ingeschrevenPersoon"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Errors) > 0 {
		t.Fatalf("graphql errors: %+v", out.Errors)
	}
	if out.Data.IngeschrevenPersoon == nil || len(out.Data.IngeschrevenPersoon.HeeftBelastingjaarAangifte) == 0 {
		t.Fatalf("no aangiften for BSN 123456789: %+v", out)
	}
	// Sanity: every aangifte has a belastingjaar, an indieningsdatum and a
	// verzamelinkomen with valuta (custom scalars over pointer fields).
	for _, r := range out.Data.IngeschrevenPersoon.HeeftBelastingjaarAangifte {
		if r.Belastingjaar == 0 {
			t.Fatalf("unexpected empty belastingjaar in %+v", r)
		}
		if r.Indieningsdatum == nil {
			t.Fatalf("missing indieningsdatum in %+v", r)
		}
		if r.Verzamelinkomen == nil {
			t.Fatalf("missing verzamelinkomen in %+v", r)
		}
		if r.Verzamelinkomen.Valuta == nil {
			t.Fatalf("missing valuta in %+v", r)
		}
	}
}

// The belastingjaren argument (demo-bron extension) filters the returned
// aangiften server-side so per-year policy enforcement has a query-side
// selector to bind to.
func TestGraphQLBelastingjarenFilter(t *testing.T) {
	if err := loadMockData("mockdata/citizens.json"); err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("graphql-server-test")
	schema, err := buildSchema(tracer)
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	srv := httptest.NewServer(newMux(&schema, tracer))
	defer srv.Close()

	body := `{"query":"query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) { heeftBelastingjaarAangifte(belastingjaren: [2025]) { belastingjaar } } }","variables":{"bsn":"123456789"}}`
	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var out struct {
		Data struct {
			IngeschrevenPersoon *struct {
				HeeftBelastingjaarAangifte []struct {
					Belastingjaar int `json:"belastingjaar"`
				} `json:"heeftBelastingjaarAangifte"`
			} `json:"ingeschrevenPersoon"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Errors) > 0 {
		t.Fatalf("graphql errors: %+v", out.Errors)
	}
	aangiften := out.Data.IngeschrevenPersoon.HeeftBelastingjaarAangifte
	if len(aangiften) != 1 || aangiften[0].Belastingjaar != 2025 {
		t.Fatalf("expected only 2025 aangifte, got %+v", aangiften)
	}
}

func TestGraphQLHealth(t *testing.T) {
	if err := loadMockData("mockdata/citizens.json"); err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("graphql-server-test")
	schema, err := buildSchema(tracer)
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	srv := httptest.NewServer(newMux(&schema, tracer))
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
