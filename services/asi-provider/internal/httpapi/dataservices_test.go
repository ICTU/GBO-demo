package httpapi

import (
	"net/http"
	"testing"

	"gbo-demo/asi-provider/internal/accesstoken"
	"gbo-demo/asi-provider/internal/seal"
)

// One data service per authentic source: a token is for one data service
// (B2), and a data service whose source is the ASIP names no authentic
// source (B6).

func TestATokenIsForOneDataService(t *testing.T) { // B2
	env := newTestServer(t)
	body := verifyBody(idDiploma, `{"qualification":"MSc"}`)
	rec := postTo(t, env, duoBase, "/verify", env.person(bsnFrouke), body)
	assertAuthError(t, rec, "invalid_token")

	claims := env.authz.claims(bsnFrouke, "verify", env.qtsp.jkt)
	claims["aud"] = testPublicURL + duoBase
	claims["authorization_details"].([]map[string]any)[0]["locations"] = []string{testPublicURL + duoBase}
	if rec := postTo(t, env, duoBase, "/verify", env.presenting(claims, nil), body); rec.Code != http.StatusOK {
		t.Fatalf("token for the duo data service at duo: status = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := post(t, env, "/verify", env.presenting(claims, nil), verifyBody(idFamilyName, `{"family_name":"Jansen"}`)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token for the duo data service at brp: status = %d, want 401", rec.Code)
	}
}

func TestADataServiceWithoutAuthenticSourceNamesNone(t *testing.T) { // B6
	env := newTestServer(t)
	claims := env.authz.claims(bsnFrouke, "verify retrieve", env.qtsp.jkt)
	claims["aud"] = testPublicURL + duoBase
	claims["authorization_details"].([]map[string]any)[0]["locations"] = []string{testPublicURL + duoBase}
	for _, op := range []struct{ path, body string }{
		{"/verify", verifyBody(idDiploma, `{"qualification":"MSc"}`)},
		{"/retrieve", `{"attributeIdentifiers":[{"attributeIdentifier":"` + idDiploma + `"}]}`},
	} {
		rec := postTo(t, env, duoBase, op.path, env.presenting(claims, nil), op.body)
		var resp map[string]any
		decode(t, rec, &resp)
		if _, ok := resp["authenticSource"]; ok || resp["provider"] == nil {
			t.Fatalf("%s: response %s, want a provider and no authenticSource", op.path, rec.Body)
		}
	}
}

func TestADataServiceIsNamedOnce(t *testing.T) {
	authz := newTestAuthorizationServer(t)
	ds := DataService{ID: "brp", Tokens: accesstoken.NewValidator(testIssuer, testPublicURL+testBase, testPublicURL, accesstoken.StaticKeys(authz.jwks))}
	if _, err := NewHandler(Config{PublicURL: testPublicURL, Sealer: &seal.Sealer{}, DataServices: []DataService{ds, ds}}); err == nil {
		t.Fatal("err = nil, want the second brp refused")
	}
	if _, err := NewHandler(Config{PublicURL: testPublicURL}); err == nil {
		t.Fatal("err = nil, want a handler without data services refused")
	}
}
