package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/getkin/kin-openapi/openapi3filter"
)

// The DIP stub of slice 6a (#533), cases R1–R7. The sources are fake ASIP
// data services; every response is checked against the Annex A OpenAPI.

const (
	idFamilyName = "https://gbo.example/attributes/family_name/1"
	idAddress    = "https://gbo.example/attributes/address/1"
	idDiploma    = "https://gbo.example/attributes/education_qualification/1"
)

func record(id, ds, country string) map[string]any {
	return map[string]any{
		"attributeIdentifier": id,
		"endpointDescription": "https://asip.test/" + ds + "/openapi.json",
		"endpointURI":         "https://asip.test/" + ds + "/authsrc-api",
		"provider":            map[string]any{"legalName": "GBO demo ASIP"},
		"country":             country,
	}
}

// source serves the records of one data service, as the ASIP does.
func source(t *testing.T, records ...map[string]any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"dataServices": records})
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/dataservices"
}

func newTestHandler(t *testing.T, sources ...string) http.Handler {
	t.Helper()
	dir, err := newDirectory(sources)
	if err != nil {
		t.Fatal(err)
	}
	h, err := newHandler(dir)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// retrieve sends a retrieve query and checks the response against Annex A.
func retrieve(t *testing.T, h http.Handler, params url.Values) (int, []map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/retrieve?"+params.Encode(), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	doc, err := loadContract()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := route(doc, "/retrieve")
	if err != nil {
		t.Fatal(err)
	}
	if err := openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{Request: req, Route: rt, Options: &openapi3filter.Options{ExcludeRequestBody: true}},
		Status:                 rec.Code,
		Header:                 rec.Header(),
		Body:                   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}); err != nil {
		t.Fatalf("response %d does not conform to Annex A: %v\n%s", rec.Code, err, rec.Body)
	}
	var out struct {
		DataServices []map[string]any `json:"dataServices"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out.DataServices
}

func params(id string, extra ...string) url.Values {
	v := url.Values{"queryType": {"dataServices"}, "attributeIdentifier": {id}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return v
}

func TestRetrieveFindsTheDataService(t *testing.T) { // R1, R2
	h := newTestHandler(t,
		source(t, record(idFamilyName, "brp", "NL"), record(idAddress, "brp", "NL")),
		source(t, record(idDiploma, "duo", "NL")))
	status, got := retrieve(t, h, params(idFamilyName))
	if status != http.StatusOK || len(got) != 1 || got[0]["endpointURI"] != "https://asip.test/brp/authsrc-api" {
		t.Fatalf("retrieve family name = %d %v, want the brp record", status, got)
	}
	status, got = retrieve(t, h, params(idDiploma))
	if status != http.StatusOK || len(got) != 1 || got[0]["endpointURI"] != "https://asip.test/duo/authsrc-api" {
		t.Fatalf("retrieve diploma = %d %v, want the duo record", status, got)
	}
	status, got = retrieve(t, h, params("https://gbo.example/attributes/shoe_size/1"))
	if status != http.StatusOK || len(got) != 0 {
		t.Fatalf("retrieve unknown attribute = %d %v, want 200 and none", status, got)
	}
}

func TestRetrieveRefusesAMalformedQuery(t *testing.T) { // R3
	for name, v := range map[string]url.Values{
		"no queryType":                  {"attributeIdentifier": {idFamilyName}},
		"other queryType":               {"queryType": {"attributes"}, "attributeIdentifier": {idFamilyName}},
		"no attributeIdentifier":        {"queryType": {"dataServices"}},
		"attributeIdentifier not a URI": {"queryType": {"dataServices"}, "attributeIdentifier": {"family_name"}},
	} {
		t.Run(name, func(t *testing.T) {
			if status, _ := retrieve(t, newTestHandler(t, source(t)), v); status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
		})
	}
}

func TestRetrieveFilters(t *testing.T) { // R4, R5
	h := newTestHandler(t, source(t, record(idFamilyName, "brp", "NL")), source(t, record(idFamilyName, "be-rrn", "BE")))
	for _, tc := range []struct {
		name  string
		extra []string
		want  int
	}{
		{"no country", nil, 2},
		{"NL", []string{"country", "NL"}, 1},
		{"DE", []string{"country", "DE"}, 0},
		{"HTTP interface", []string{"conformsTo", "urn:ietf:rfc:9110"}, 2},
		{"ISO 15000 interface", []string{"conformsTo", "urn:iso:std:iso:15000"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status, got := retrieve(t, h, params(idFamilyName, tc.extra...)); status != http.StatusOK || len(got) != tc.want {
				t.Fatalf("= %d with %d records, want %d", status, len(got), tc.want)
			}
		})
	}
}

// An ASIP that cannot be read, or whose records do not conform to the ETSI
// schema, is left out; the others still answer (R6).
func TestAnUnreachableSourceIsLeftOut(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	t.Cleanup(broken.Close)
	// A failing source whose error body happens to look like records.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"dataServices": []any{record(idFamilyName, "stale", "NL")}})
	}))
	t.Cleanup(failing.Close)
	// Records missing their required members, and one with a member the
	// schema does not allow.
	incomplete := source(t, map[string]any{"attributeIdentifier": idFamilyName, "country": "NL"})
	extra := record(idFamilyName, "extra", "NL")
	extra["unexpected"] = true
	h := newTestHandler(t, down.URL+"/dataservices", broken.URL, failing.URL, incomplete, source(t, extra),
		source(t, record(idFamilyName, "brp", "NL")))
	if status, got := retrieve(t, h, params(idFamilyName)); status != http.StatusOK || len(got) != 1 {
		t.Fatalf("= %d %v, want the reachable record", status, got)
	}
}

func TestSearchIsNotOffered(t *testing.T) { // R7
	rec := httptest.NewRecorder()
	newTestHandler(t, source(t)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/search?assetType=attribute", nil))
	if rec.Code != http.StatusNotImplemented || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("GET /search = %d %s, want 501 problem", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestTheContractIsPublished(t *testing.T) {
	h := newTestHandler(t, source(t))
	for _, path := range []string{"/openapi.json", "/19478-dataservice-schema.json", "/19478-attribute-schema.json"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
			t.Errorf("GET %s = %d", path, rec.Code)
		}
	}
}
