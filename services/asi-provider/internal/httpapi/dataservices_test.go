package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	asimapping "gbo-demo/asi-mapping"
	"gbo-demo/asi-provider/internal/accesstoken"
	"gbo-demo/asi-provider/internal/seal"
)

// One data service per authentic source: a token is for one data service
// (B2).

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

// Verify and retrieve name the authentic source of the source's description;
// the provider is the ASIP (E4).
func TestTheResponsesNameTheDescribedSource(t *testing.T) { // E4
	env := newTestServer(t)
	duo := env.authz.claims(bsnFrouke, "verify retrieve", env.qtsp.jkt)
	duo["aud"] = testPublicURL + duoBase
	duo["authorization_details"].([]map[string]any)[0]["locations"] = []string{testPublicURL + duoBase}
	for _, tc := range []struct {
		base                   string
		verify, retrieve       string
		provider, authenticSrc string
	}{
		{testBase, verifyBody(idFamilyName, `{"family_name":"Jansen"}`), retrieveBody(idFamilyName), "GBO demo ASIP (test)", "Basisregistratie Personen (mock)"},
		{duoBase, verifyBody(idDiploma, `{"qualification":"MSc"}`), retrieveBody(idDiploma), "DUO (test)", "Diplomaregister (test)"},
	} {
		for _, op := range []struct{ path, body string }{{"/verify", tc.verify}, {"/retrieve", tc.retrieve}} {
			presenting := env.person(bsnFrouke)
			if tc.base == duoBase {
				presenting = env.presenting(duo, nil)
			}
			rec := postTo(t, env, tc.base, op.path, presenting, op.body)
			provider, source := names(t, rec)
			if provider != tc.provider || source != tc.authenticSrc {
				t.Errorf("%s%s: provider %q, authenticSource %q; want %q, %q", tc.base, op.path, provider, source, tc.provider, tc.authenticSrc)
			}
		}
	}
}

// A new release with another legal name changes the name in the next
// response (E5). A release that changes while the source is asked does not
// change the response: it names the source of the description the query
// came from (E6).
func TestANewReleaseChangesTheName(t *testing.T) {
	renamed := described(func(asi *asimapping.Capability) { asi.AuthenticSource.LegalName = "BRP (renamed)" })
	for _, tc := range []struct {
		name       string
		asi, next  *asimapping.Capability
		wantSource string
	}{
		{"E5 the next response", renamed, nil, "BRP (renamed)"},
		{"E6 a release between query and response", brpASI, renamed, "Basisregistratie Personen (mock)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, op := range []struct{ path, body string }{
				{"/verify", verifyBody(idFamilyName, `{"family_name":"Jansen"}`)},
				{"/retrieve", retrieveBody(idFamilyName)},
			} {
				source := &releases{fakeSource: testPersons}
				authz, qtsp := newTestAuthorizationServer(t), newTestQTSP(t)
				env := newTestServerWithSource(t, authz, qtsp, accesstoken.StaticKeys(authz.jwks), source)
				source.asi, source.next = tc.asi, tc.next
				if _, got := names(t, post(t, env, op.path, env.person(bsnFrouke), op.body)); got != tc.wantSource {
					t.Errorf("%s: authenticSource %q, want %q", op.path, got, tc.wantSource)
				}
			}
		})
	}
}

func retrieveBody(id string) string {
	return `{"attributeIdentifiers":[{"attributeIdentifier":"` + id + `"}]}`
}

// names returns the legal names of the provider and the authentic source in
// a response.
func names(t *testing.T, rec *httptest.ResponseRecorder) (provider, authenticSource string) {
	t.Helper()
	var resp struct {
		Provider        *Provider `json:"provider"`
		AuthenticSource *Provider `json:"authenticSource"`
	}
	decode(t, rec, &resp)
	if resp.Provider == nil || resp.AuthenticSource == nil {
		t.Fatalf("response %s, want a provider and an authenticSource", rec.Body)
	}
	return resp.Provider.LegalName, resp.AuthenticSource.LegalName
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
