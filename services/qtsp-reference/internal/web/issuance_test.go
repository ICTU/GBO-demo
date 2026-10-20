package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gbo-demo/qtsp-reference/internal/issuance"
	"gbo-demo/qtsp-reference/internal/login"
	"gbo-demo/qtsp-reference/internal/qtsp"
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
		Results: []row{{Identifier: DemoAttributes[0].Identifier, Label: "Familienaam", Result: "Match", Value: []byte(`{"family_name":"Jansen"}`)}},
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
