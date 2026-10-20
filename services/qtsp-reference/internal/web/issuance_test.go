package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gbo-demo/qtsp-reference/internal/issuance"
	"gbo-demo/qtsp-reference/internal/issuancecallback"
	"gbo-demo/qtsp-reference/internal/login"
	"gbo-demo/qtsp-reference/internal/qtsp"
	"gbo-demo/qtsp-reference/internal/seal"
)

func TestResultOffersWalletIssuanceOnlyForSuccessfulEvidence(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store := issuance.NewStore(func() time.Time { return now })
	h := newHandler(nil, DemoAttributes, "http://localhost:4023", nil)
	h.now, h.issuance, h.issuanceLink = func() time.Time { return now }, store, "https://wallet.example/issue"
	identity := login.Identity{FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"NL"}}
	requested := []qtsp.Attribute{{Identifier: DemoAttributes[0].Identifier}}

	success := outcome{
		Status: statusDone, Operation: "verify", Source: "RvIG", Seal: &sealView{Valid: true},
		Results:  []row{{Identifier: DemoAttributes[0].Identifier, Label: "Familienaam", Result: "Match"}},
		Evidence: []issuance.Evidence{{Name: "Familienaam", Outcome: "Match", Value: []byte(`{"family_name":"Jansen"}`)}},
	}
	if image := h.offerIssuance(identity, requested, success); !strings.HasPrefix(string(image), "data:image/png;base64,") {
		t.Fatalf("successful result image = %q, want PNG data URL", image)
	}

	for _, failed := range []outcome{
		{Status: statusDone, Operation: "verify", Source: "RvIG", Seal: &sealView{Valid: true}, Results: []row{{Identifier: DemoAttributes[0].Identifier, Label: "Familienaam", Result: "NoMatch", Value: []byte(`"Jansen"`)}}},
		{Status: statusDone, Operation: "verify", Source: "RvIG", Seal: &sealView{Valid: false}, Results: []row{{Identifier: DemoAttributes[0].Identifier, Label: "Familienaam", Result: "Match", Value: []byte(`"Jansen"`)}}},
		{Status: statusFailed, Operation: "verify", Source: "RvIG", Seal: &sealView{Valid: true}, Results: []row{{Identifier: DemoAttributes[0].Identifier, Label: "Familienaam", Result: "Match", Value: []byte(`"Jansen"`)}}},
		{Status: statusDone, Operation: "verify", Source: "RvIG", Seal: &sealView{Valid: true}},
	} {
		if image := h.offerIssuance(identity, requested, failed); image != "" {
			t.Errorf("failed result image = %q, want empty", image)
		}
	}
}

func TestAcceptedResultFlowsFromResultPageToIssuanceCallback(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	store := issuance.NewStore(func() time.Time { return now })
	h := newHandler(nil, DemoAttributes, "http://localhost:4023", nil)
	h.now, h.issuance, h.issuanceLink = func() time.Time { return now }, store, "https://wallet.example/issue"
	identity := login.Identity{FamilyName: "Jansen", GivenName: "Frouke", Birthdate: "2000-03-24", Nationalities: []string{"NL"}}
	requested := []qtsp.Attribute{{Identifier: DemoAttributes[0].Identifier, Value: json.RawMessage(`{"family_name":"Jansen"}`)}}
	prepared := prepared{operation: "verify", attributes: requested}
	response := qtsp.Response{
		Status: http.StatusOK,
		Body:   []byte(`{"attributeVerificationResults":[{"attributeIdentifier":"https://gbo.example/attributes/family_name/1","attributeVerificationResult":"http://uri.etsi.org/19478/VerificationResult/Match"}],"authenticSource":{"legalName":"RvIG"}}`),
		Seal:   &seal.Check{Signer: "test source"},
	}

	result := h.result(prepared, response)
	if len(result.Results) != 1 || len(result.Results[0].Value) != 0 || len(result.Evidence) != 1 {
		t.Fatalf("result rows/evidence = %+v / %+v", result.Results, result.Evidence)
	}
	apiJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(apiJSON), `"value"`) {
		t.Fatalf("verify API leaked QTSP evidence value: %s", apiJSON)
	}
	image := h.offerIssuance(identity, requested, result)
	if !strings.HasPrefix(string(image), "data:image/png;base64,") {
		t.Fatalf("wallet image = %q", image)
	}
	page := httptest.NewRecorder()
	render(page, http.StatusOK, resultPage, resultView{outcome: result, Who: &identity, WalletQR: image})
	if !strings.Contains(page.Body.String(), "Zet de verklaring in uw NL Wallet") {
		t.Fatal("result page has no wallet issuance action")
	}

	callbackToken := "test-callback-token"
	body := `[{"id":"pid","attestations":[{"attestation_type":"urn:eudi:pid:nl:1","attributes":{"urn:eudi:pid:nl:1":{"family_name":"Jansen","given_name":"Frouke","birthdate":"2000-03-24","nationalities":["NL"]}}}]}]`
	req := httptest.NewRequest(http.MethodPost, issuancecallback.Path(callbackToken), strings.NewReader(body))
	recorder := httptest.NewRecorder()
	issuancecallback.Handler(store, callbackToken).ServeHTTP(recorder, req)
	var documents []issuance.Document
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &documents) != nil || len(documents) != 1 {
		t.Fatalf("callback response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestResultPageMarksTheWalletAttestationAsNonQualifiedTest(t *testing.T) {
	recorder := httptest.NewRecorder()
	render(recorder, 200, resultPage, resultView{
		outcome:  outcome{Status: statusDone, Operation: "verify", Source: "RvIG", Seal: &sealView{Valid: true}},
		WalletQR: "data:image/png;base64,dGVzdA==",
	})
	body := recorder.Body.String()
	for _, text := range []string{"Zet de verklaring in uw NL Wallet", "test", "geen gekwalificeerde"} {
		if !strings.Contains(body, text) {
			t.Errorf("result page does not contain %q", text)
		}
	}
}
