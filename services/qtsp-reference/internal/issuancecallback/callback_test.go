package issuancecallback

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gbo-demo/qtsp-reference/internal/issuance"
	"gbo-demo/qtsp-reference/internal/login"
)

func TestCallbackExchangesNestedPIDForOneDocument(t *testing.T) {
	store := issuance.NewStore(time.Now)
	store.Add(issuance.Request{
		Identity:  login.Identity{FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"NL", "BE"}},
		Operation: "verify", Source: "RvIG",
		Evidence: []issuance.Evidence{{Name: "Familienaam", Outcome: "Match", Value: json.RawMessage(`"Jansen"`)}}, ExpectedAttributes: 1,
	})

	response := post(t, store, pidDisclosure(`{"family_name":"Jansen","given_name":"Frouke","birthdate":"2000-03-24","nationalities":["BE","NL"]}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var documents []issuance.Document
	if err := json.Unmarshal(response.Body.Bytes(), &documents); err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].AttestationType != issuance.AttestationType {
		t.Fatalf("documents = %+v, want one %s", documents, issuance.AttestationType)
	}
}

func TestCallbackReturnsEmptyForDifferentPID(t *testing.T) {
	store := issuance.NewStore(time.Now)
	store.Add(issuance.Request{
		Identity:  login.Identity{FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"NL"}},
		Operation: "retrieve", Source: "RvIG",
		Evidence: []issuance.Evidence{{Name: "Adres", Value: json.RawMessage(`{"city":"Rotterdam"}`)}}, ExpectedAttributes: 1,
	})
	response := post(t, store, pidDisclosure(`{"family_name":"Bakker","given_name":"Frouke","birthdate":"2000-03-24","nationalities":["NL"]}`))
	if got := strings.TrimSpace(response.Body.String()); response.Code != http.StatusOK || got != "[]" {
		t.Fatalf("response = %d %s, want 200 []", response.Code, got)
	}
}

func TestCallbackRejectsMalformedInputAndOtherRoutes(t *testing.T) {
	store := issuance.NewStore(time.Now)
	bad := post(t, store, `{}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed status = %d, want 400", bad.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/other", strings.NewReader("[]"))
	rec := httptest.NewRecorder()
	Handler(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", rec.Code)
	}
}

func pidDisclosure(attributes string) string {
	return `[{"id":"pid","attestations":[{"attestation_type":"urn:eudi:pid:nl:1","attributes":{"urn:eudi:pid:nl:1":` + attributes + `}}]}]`
}

func post(t *testing.T, store *issuance.Store, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, AttestationPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	Handler(store).ServeHTTP(rec, req)
	return rec
}
