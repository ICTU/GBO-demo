package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublishedTypeMetadataIsBoundToItsExactBytes(t *testing.T) {
	definition := sourceAttestationDefinition{
		TypeID:      "inkomensverklaring",
		TypeVersion: "1.0",
		TypeMetadata: json.RawMessage(`{
			"name":"Inkomensverklaring",
			"schema":{"type":"object"}
		}`),
	}
	publication, err := newTypeMetadataPublication(
		"https://issuer.example",
		"99999999900000000200",
		definition,
	)
	if err != nil {
		t.Fatalf("new type metadata publication: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, publication.VCT, nil)
	recorder := httptest.NewRecorder()
	publication.ServeHTTP(recorder, req)
	resp := recorder.Result()
	defer resp.Body.Close()
	if got, want := resp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got, want := resp.Header.Get("Content-Type"), "application/json"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Error("published Type Metadata has no ETag")
	} else {
		conditional := httptest.NewRequest(http.MethodGet, publication.VCT, nil)
		conditional.Header.Set("If-None-Match", etag)
		conditionalRecorder := httptest.NewRecorder()
		publication.ServeHTTP(conditionalRecorder, conditional)
		if got, want := conditionalRecorder.Code, http.StatusNotModified; got != want {
			t.Errorf("conditional status = %d, want %d", got, want)
		}
	}
	if got, want := publication.VCT, "https://issuer.example/types/99999999900000000200/inkomensverklaring/v1.0"; got != want {
		t.Errorf("VCT = %q, want %q", got, want)
	}

	body := recorder.Body.Bytes()
	var metadata map[string]any
	if err := json.Unmarshal(body, &metadata); err != nil {
		t.Fatalf("decode published Type Metadata: %v", err)
	}
	if got := metadata["vct"]; got != publication.VCT {
		t.Errorf("published vct = %v, want %q", got, publication.VCT)
	}
	schema := metadata["schema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	if properties["vct"] == nil || properties["vct#integrity"] == nil {
		t.Fatalf("managed credential claims are missing from schema: %v", properties)
	}
	digest := sha256.Sum256(body)
	wantIntegrity := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
	if got := publication.Integrity; got != wantIntegrity {
		t.Errorf("integrity = %q, want %q", got, wantIntegrity)
	}
}

func TestSourceCannotSupplyVCTOrIntegrity(t *testing.T) {
	for _, forbidden := range []string{"vct", "vct#integrity"} {
		t.Run(forbidden, func(t *testing.T) {
			definition := sourceAttestationDefinition{
				TypeID:       "inkomensverklaring",
				TypeVersion:  "1.0",
				TypeMetadata: json.RawMessage(`{"name":"Inkomensverklaring","` + forbidden + `":"source-controlled"}`),
			}
			if _, err := newTypeMetadataPublication("https://issuer.example", "99999999900000000200", definition); err == nil {
				t.Fatalf("source-controlled %q was accepted", forbidden)
			}
		})
	}
}

func TestSummaryPlaceholdersRequireMatchingClaimSVGIDs(t *testing.T) {
	definition := sourceAttestationDefinition{
		TypeID:      "inkomensverklaring",
		TypeVersion: "1.0",
		TypeMetadata: json.RawMessage(`{
			"display":[{"lang":"nl-NL","summary":"€{{verzamelinkomen}} ({{belastingjaar}})"}],
			"claims":[
				{"path":["belastingjaar"],"svg_id":"belastingjaar"},
				{"path":["verzamelinkomen"]}
			],
			"schema":{"type":"object"}
		}`),
	}
	if _, err := newTypeMetadataPublication("https://issuer.example", "99999999900000000200", definition); err == nil {
		t.Fatal("summary placeholder without matching svg_id was accepted")
	}

	definition.TypeMetadata = json.RawMessage(`{
		"display":[{"lang":"nl-NL","summary":"€{{verzamelinkomen}} ({{belastingjaar}})"}],
		"claims":[
			{"path":["belastingjaar"],"svg_id":"belastingjaar"},
			{"path":["verzamelinkomen"],"svg_id":"verzamelinkomen"}
		],
		"schema":{"type":"object"}
	}`)
	if _, err := newTypeMetadataPublication("https://issuer.example", "99999999900000000200", definition); err != nil {
		t.Fatalf("matching summary svg_id values were rejected: %v", err)
	}
}
