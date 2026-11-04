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

const testCallbackToken = "test-callback-token"

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
	for _, path := range []string{"/other", "/attestations"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("[]"))
		rec := httptest.NewRecorder()
		Handler(store, testCallbackToken).ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unknown route %s status = %d, want 404", path, rec.Code)
		}
	}
}

// The own credential issuer fetches the result the QTSP named in its offer:
// once, and an unknown or used reference is 404 (no data).
func TestCallbackExchangesOfferReferenceForOneDocument(t *testing.T) {
	store := issuance.NewStore(time.Now)
	ref := store.Add(issuance.Request{
		Identity:  login.Identity{FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"NL"}},
		Operation: "verify", Source: "RvIG",
		Evidence: []issuance.Evidence{{Name: "Familienaam", Outcome: "Match", Value: json.RawMessage(`"Jansen"`)}}, ExpectedAttributes: 1,
	})
	body := `{"subject":{"qtsp_request":"` + ref + `"}}`
	response := post(t, store, body)
	var documents []issuance.Document
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &documents) != nil ||
		len(documents) != 1 || documents[0].AttestationType != issuance.AttestationType {
		t.Fatalf("response = %d %s, want one document", response.Code, response.Body.String())
	}
	for _, again := range []string{body, `{"subject":{"qtsp_request":"unknown"}}`} {
		if response := post(t, store, again); response.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", again, response.Code)
		}
	}
	for _, bad := range []string{`{"subject":{"bsn":"999990019"}}`, `{"subject":{"qtsp_request":1}}`} {
		if response := post(t, store, bad); response.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", bad, response.Code)
		}
	}
}

func pidDisclosure(attributes string) string {
	return `[{"id":"pid","attestations":[{"attestation_type":"urn:eudi:pid:nl:1","attributes":{"urn:eudi:pid:nl:1":` + attributes + `}}]}]`
}

func post(t *testing.T, store *issuance.Store, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, Path(testCallbackToken), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	Handler(store, testCallbackToken).ServeHTTP(rec, req)
	return rec
}
