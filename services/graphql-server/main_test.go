package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
)

// Happy-path integration test: load the demo mock data, build the schema,
// spin up the mux behind an httptest.Server, and issue a GraphQL query for
// the pre-baked BSN 123456789. Verifies wiring: /graphql handler → schema
// resolver → mock store.
func TestGraphQLHappyPath(t *testing.T) {
	store, err := loadMockData("mockdata/citizens.json")
	if err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("graphql-server-test")
	schema, err := buildSchema(tracer, store)
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
	store, err := loadMockData("mockdata/citizens.json")
	if err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("graphql-server-test")
	schema, err := buildSchema(tracer, store)
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

// testMux spins up the routing tree over the demo mock data.
func testMux(t *testing.T) *http.ServeMux {
	t.Helper()
	store, err := loadMockData("mockdata/citizens.json")
	if err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("graphql-server-test")
	schema, err := buildSchema(tracer, store)
	if err != nil {
		t.Fatalf("buildSchema: %v", err)
	}
	return newMux(&schema, tracer)
}

// This server serves GraphQL and nothing else: the playground moved to the
// developer-portal, which queries /graphql over its own proxy. A browser
// opening /graphql therefore gets GraphQL, not a page and not a redirect to
// one that no longer exists.
func TestGraphQLServesNoUI(t *testing.T) {
	srv := httptest.NewServer(testMux(t))
	defer srv.Close()

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	query := `{ingeschrevenPersoon(bsn:"123456789"){bsn}}`
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/graphql?query="+url.QueryEscape(query), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(body), "<html") {
		t.Fatalf("browser GET /graphql served HTML; this server has no UI:\n%s", string(body))
	}
	if !strings.Contains(string(body), `"123456789"`) {
		t.Fatalf("expected GraphQL data, got %s", string(body))
	}

	// The page it used to redirect to is gone with it.
	pg, err := http.Get(srv.URL + "/playground")
	if err != nil {
		t.Fatalf("get playground: %v", err)
	}
	defer pg.Body.Close()
	if pg.StatusCode != http.StatusNotFound {
		t.Fatalf("/playground status = %d, want 404", pg.StatusCode)
	}
}

// The machine paths keep working exactly as before: the FSC-Inway POSTs, and
// `GET ?raw` is the escape hatch for a browser that wants JSON.
func TestGraphQLMachinePaths(t *testing.T) {
	srv := httptest.NewServer(testMux(t))
	defer srv.Close()

	query := `{ingeschrevenPersoon(bsn:"123456789"){bsn}}`
	cases := []struct {
		name   string
		method string
		url    string
		accept string
	}{
		{"post from a client", http.MethodPost, srv.URL + "/graphql", ""},
		{"browser asking for raw", http.MethodGet, srv.URL + "/graphql?raw&query=" + url.QueryEscape(query), "text/html"},
		{"client accepting json", http.MethodGet, srv.URL + "/graphql?query=" + url.QueryEscape(query), "application/json,text/html"},
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.method == http.MethodPost {
				payload, err := json.Marshal(map[string]string{"query": query})
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				body = bytes.NewReader(payload)
			}
			req, err := http.NewRequest(tc.method, tc.url, body)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			if tc.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			var out struct {
				Data struct {
					IngeschrevenPersoon *struct {
						BSN string `json:"bsn"`
					} `json:"ingeschrevenPersoon"`
				} `json:"data"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Data.IngeschrevenPersoon == nil || out.Data.IngeschrevenPersoon.BSN != "123456789" {
				t.Fatalf("expected GraphQL data, got %+v", out)
			}
		})
	}
}

func TestGraphQLHealth(t *testing.T) {
	store, err := loadMockData("mockdata/citizens.json")
	if err != nil {
		t.Fatalf("loadMockData: %v", err)
	}
	tracer := otel.Tracer("graphql-server-test")
	schema, err := buildSchema(tracer, store)
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
