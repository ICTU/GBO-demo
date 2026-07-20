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

	body := `{"query":"query($bsn: String!) { inkomensgegevens(input:{burgerservicenummer:$bsn}) { belastingjaar verzamelinkomen } }","variables":{"bsn":"123456789"}}`
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
			Inkomensgegevens []struct {
				Belastingjaar   int `json:"belastingjaar"`
				Verzamelinkomen int `json:"verzamelinkomen"`
			} `json:"inkomensgegevens"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Errors) > 0 {
		t.Fatalf("graphql errors: %+v", out.Errors)
	}
	if len(out.Data.Inkomensgegevens) == 0 {
		t.Fatalf("no records for BSN 123456789: %+v", out)
	}
	// Sanity: at least one record with belastingjaar > 0.
	for _, r := range out.Data.Inkomensgegevens {
		if r.Belastingjaar == 0 {
			t.Fatalf("unexpected empty belastingjaar in %+v", r)
		}
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
