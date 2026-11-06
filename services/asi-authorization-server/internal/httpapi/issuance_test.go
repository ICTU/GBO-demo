package httpapi

// A credential issuer as client (#614, cases Z1-Z13): a source's PuB-EAA
// provider logs the citizen in here before it issues the source's
// attestation to a wallet. It registers with the PUB_EAA_Provider
// entitlement and the issuance scope, and gets tokens only for its own
// issuance resource. The ASI part stays as it is: a token for a data service
// of the ASIP goes only to a QEAA_Provider (verify, retrieve).

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

const (
	pubEAAProvider = "https://uri.etsi.org/19475/Entitlement/PUB_EAA_Provider"

	issuerRegistryURI      = "https://register.test/wrp/belastingdienst"
	otherIssuerRegistryURI = "https://register.test/wrp/rvig"
	bothRegistryURI        = "https://register.test/wrp/both"
	issuerOrganizationID   = "NTRNL-99990001"
	otherIssuerOrgID       = "NTRNL-99990002"
	bothOrganizationID     = "NTRNL-99990003"

	issuanceResource      = "https://issuer.test/belastingdienst/eudi"
	otherIssuanceResource = "https://issuer.test/rvig/eudi"
	issuerRedirectURI     = "https://issuer.test/belastingdienst/eudi/callback"
)

// registerIssuer registers the PuB-EAA provider of orgID through its entry
// registryURI with scope issuance, and returns its client_id.
func (as *testAS) registerIssuer(t *testing.T, registryURI, orgID string) string {
	t.Helper()
	q := as.ca.issue(t, orgID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	body := issuerRegistrationBody(as.statement(t, q, func(c map[string]any) { c["registryURI"] = registryURI }), "issuance")
	r := as.postRegistration(t, body)
	if r.status != http.StatusCreated {
		t.Fatalf("registration = %d %v, want 201", r.status, r.body)
	}
	id, _ := r.body["client_id"].(string)
	var jwks struct {
		Keys []struct {
			KeyID string `json:"kid"`
		} `json:"keys"`
	}
	raw, _ := json.Marshal(r.body["jwks"])
	_ = json.Unmarshal(raw, &jwks)
	as.keys[id] = q.key
	as.kids[id] = jwks.Keys[0].KeyID
	return id
}

func issuerRegistrationBody(statement, scope string) map[string]any {
	return map[string]any{"redirect_uris": []string{issuerRedirectURI}, "scope": scope, "software_statement": statement}
}

// issuanceParams is an authorization request of client for resource with
// scope issuance, without authorization_details.
func issuanceParams(p pkce, client, resource string) url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {client},
		"redirect_uri":          {issuerRedirectURI},
		"scope":                 {"issuance"},
		"state":                 {"state-123"},
		"code_challenge":        {p.challenge},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}
}

// assertIssuerAuthorizeError checks a redirect to the issuer with error code.
func assertIssuerAuthorizeError(t *testing.T, resp *http.Response, code string) {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorization response = %d, want 302 to the issuer", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), issuerRedirectURI) {
		t.Fatalf("Location = %q, want the issuer's redirect_uri", resp.Header.Get("Location"))
	}
	if q := loc.Query(); q.Get("error") != code || q.Get("code") != "" || q.Get("state") != "state-123" {
		t.Fatalf("redirect = %v, want error %q, no code, state echoed", q, code)
	}
}

// --- Z1-Z5: registration --------------------------------------------------------

func TestAPubEAAProviderRegistersForIssuance(t *testing.T) { // Z1
	as := newTestAS(t)
	q := as.ca.issue(t, issuerOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	r := as.postRegistration(t, issuerRegistrationBody(as.statement(t, q, func(c map[string]any) { c["registryURI"] = issuerRegistryURI }), "issuance"))
	if r.status != http.StatusCreated || r.body["scope"] != "issuance" {
		t.Fatalf("registration = %d %v, want 201 with scope issuance", r.status, r.body)
	}
}

// The scope must fit the entitlement: issuance for a PuB-EAA provider,
// verify and retrieve for a QTSP, never both in one client.
func TestTheScopeMustFitTheEntitlement(t *testing.T) { // Z2, Z3
	as := newTestAS(t)
	issuer := as.ca.issue(t, issuerOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	qtsp := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	both := as.ca.issue(t, bothOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	for name, tc := range map[string]struct {
		cert        qtspCert
		registryURI string
		scope       string
	}{
		"Z2: PuB-EAA provider asks verify":     {issuer, issuerRegistryURI, "verify"},
		"Z2: PuB-EAA provider asks retrieve":   {issuer, issuerRegistryURI, "retrieve"},
		"Z3: QTSP asks issuance":               {qtsp, qtspRegistryURI, "issuance"},
		"both entitlements, issuance + verify": {both, bothRegistryURI, "issuance verify"},
	} {
		t.Run(name, func(t *testing.T) {
			statement := as.statement(t, tc.cert, func(c map[string]any) { c["registryURI"] = tc.registryURI })
			assertRegistrationError(t, as.postRegistration(t, issuerRegistrationBody(statement, tc.scope)), http.StatusBadRequest, "invalid_client_metadata")
		})
	}
	// An entry with both entitlements registers a client per role.
	for _, scope := range []string{"issuance", "verify"} {
		statement := as.statement(t, both, func(c map[string]any) { c["registryURI"] = bothRegistryURI })
		if r := as.postRegistration(t, issuerRegistrationBody(statement, scope)); r.status != http.StatusCreated {
			t.Fatalf("registration with both entitlements and scope %s = %d %v, want 201", scope, r.status, r.body)
		}
	}
}

func TestAnEntryWithoutEitherEntitlementIsUnapproved(t *testing.T) { // Z5
	as := newTestAS(t)
	q := as.ca.issue(t, qtspOrganizationID, x509.KeyUsageDigitalSignature, time.Now().Add(time.Hour))
	for _, scope := range []string{"verify", "issuance"} {
		statement := as.statement(t, q, func(c map[string]any) { c["registryURI"] = providerRegistryURI })
		assertRegistrationError(t, as.postRegistration(t, issuerRegistrationBody(statement, scope)), http.StatusBadRequest, "unapproved_software_statement")
	}
}

// --- Z6-Z9: the authorization request --------------------------------------------

// A token is for the role the client registered for, and an issuer gets one
// only for its own issuance resource.
func TestResourcesFollowTheRole(t *testing.T) { // Z6, Z7, Z8
	as := newTestAS(t)
	issuer := as.registerIssuer(t, issuerRegistryURI, issuerOrganizationID)
	qtsp := as.registerQTSP(t)

	// Z6: a PuB-EAA provider asks for a data service of the ASIP.
	verify := authorizeParams(newPKCE())
	verify.Set("client_id", issuer)
	verify.Set("redirect_uri", issuerRedirectURI)
	assertIssuerAuthorizeError(t, as.authorize(t, verify), "invalid_scope")
	toDataService := issuanceParams(newPKCE(), issuer, testResource)
	assertIssuerAuthorizeError(t, as.authorize(t, toDataService), "invalid_target")

	// Z7: a QTSP asks for an issuance resource.
	qtspParams := authorizeParams(newPKCE())
	qtspParams.Set("client_id", qtsp)
	qtspParams.Set("resource", issuanceResource)
	qtspParams.Del(authorization.AuthorizationDetailsClaim)
	assertAuthorizeError(t, as.authorize(t, qtspParams), "invalid_target")
	qtspParams.Set("scope", "issuance")
	assertAuthorizeError(t, as.authorize(t, qtspParams), "invalid_scope")

	// Z8: the issuer of one source asks for the issuance resource of another.
	assertIssuerAuthorizeError(t, as.authorize(t, issuanceParams(newPKCE(), issuer, otherIssuanceResource)), "invalid_target")
}

// authorization_details are an ASI construct: an issuance request carries none.
func TestAnIssuanceRequestCarriesNoAuthorizationDetails(t *testing.T) {
	as := newTestAS(t)
	issuer := as.registerIssuer(t, issuerRegistryURI, issuerOrganizationID)
	params := issuanceParams(newPKCE(), issuer, issuanceResource)
	setAuthorizationDetails(params, "https://gbo.example/attributes/family_name/1")
	assertIssuerAuthorizeError(t, as.authorize(t, params), "invalid_request")
}

func TestTheRequestPageForIssuance(t *testing.T) { // Z9
	as := newTestAS(t)
	issuer := as.registerIssuer(t, issuerRegistryURI, issuerOrganizationID)
	tx := as.startAuthorization(t, issuanceParams(newPKCE(), issuer, issuanceResource))
	for _, want := range []string{
		"<h1>Toestemming voor Belastingdienst</h1>",
		"U vraagt bij Belastingdienst een attestatie aan voor uw wallet. Belastingdienst haalt daarvoor uw gegevens op.",
		`value="digid"`, `value="deny"`,
	} {
		if !strings.Contains(tx.page, want) {
			t.Errorf("request page lacks %q:\n%s", want, tx.page)
		}
	}
	for _, unwanted := range []string{"vertrouwensdienst", "controleren", "opvragen", "verklaring"} {
		if strings.Contains(tx.page, unwanted) {
			t.Errorf("request page for issuance speaks of %q:\n%s", unwanted, tx.page)
		}
	}
}

// --- Z10-Z13 ------------------------------------------------------------------------

func TestAnIssuerGetsATokenForItsResource(t *testing.T) { // Z10
	as := newTestAS(t)
	issuer := as.registerIssuer(t, issuerRegistryURI, issuerOrganizationID)
	p := newPKCE()
	params := issuanceParams(p, issuer, issuanceResource)
	code := as.codeForIssuer(t, params, bsnFrouke)
	form := as.tokenForm(t, code, p)
	form.Set("redirect_uri", issuerRedirectURI)
	form.Set("client_assertion", as.assertion(t, issuer, nil))
	r := as.postToken(t, form, as.proof(t, nil))
	if r.status != http.StatusOK || r.body["scope"] != "issuance" {
		t.Fatalf("token response = %d %v, want 200 with scope issuance", r.status, r.body)
	}
	token, _ := r.body["access_token"].(string)
	_, claims := verifyAccessToken(t, as, token)
	for field, want := range map[string]any{
		"aud":                  issuanceResource,
		"client_id":            issuer,
		"scope":                "issuance",
		authorization.BSNClaim: bsnFrouke,
		"acr":                  "digid-mock",
	} {
		if claims[field] != want {
			t.Errorf("%s = %v, want %v", field, claims[field], want)
		}
	}
	if cnf, _ := claims["cnf"].(map[string]any); cnf["jkt"] != as.jkt {
		t.Errorf("cnf = %v, want jkt %s", claims["cnf"], as.jkt)
	}
	if v, ok := claims[authorization.AuthorizationDetailsClaim]; ok {
		t.Errorf("authorization_details = %v, want none", v)
	}
}

func TestDenyingIssuanceGivesAccessDenied(t *testing.T) { // Z11
	as := newTestAS(t)
	issuer := as.registerIssuer(t, issuerRegistryURI, issuerOrganizationID)
	tx := as.startAuthorization(t, issuanceParams(newPKCE(), issuer, issuanceResource))
	assertIssuerAuthorizeError(t, as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"deny"}}), "access_denied")
}

func TestRecheckDeactivatesAnIssuerWithoutPubEAAProvider(t *testing.T) { // Z12
	as := newTestAS(t)
	issuer := as.registerIssuer(t, issuerRegistryURI, issuerOrganizationID)
	as.register.set(issuerRegistryURI, authorization.RegisterEntry{Identifiers: []string{issuerOrganizationID}, LegalName: "Belastingdienst", Entitlements: []string{authorization.QEAAProviderEntitlement}})
	if res := as.service.RecheckEntitlements(context.Background()); res.Deactivated != 1 {
		t.Fatalf("recheck = %+v, want the issuer deactivated", res)
	}
	resp := as.authorize(t, issuanceParams(newPKCE(), issuer, issuanceResource))
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Fatalf("authorization request of a deactivated issuer = %d, Location %q; want 400 without redirect", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestMetadataListsTheIssuanceScope(t *testing.T) { // Z13
	as := newTestAS(t)
	resp, err := http.Get(as.srv.URL + authorization.MetadataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var meta struct {
		Scopes []string `json:"scopes_supported"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if strings.Join(meta.Scopes, " ") != "verify retrieve issuance" {
		t.Fatalf("scopes_supported = %v, want verify, retrieve and issuance", meta.Scopes)
	}
}

// codeForIssuer logs in with the DigiD mock with bsn and returns the code
// sent to the issuer.
func (as *testAS) codeForIssuer(t *testing.T, params url.Values, bsn string) string {
	t.Helper()
	tx := as.startAuthorization(t, params)
	if resp := as.decide(t, tx.id, tx.cookie, url.Values{"decision": {"digid"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("choosing DigiD = %d, want 303 to its login page", resp.StatusCode)
	}
	resp := as.digidLogin(t, tx.id, tx.cookie, bsn)
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(loc.String(), issuerRedirectURI) || loc.Query().Get("code") == "" {
		t.Fatalf("login = %d, Location %q; want a code at the issuer", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

// setCredentialDetails names credential configurations in authorization_details
// (RFC 9396, type openid_credential of OpenID4VCI 1.0).
func setCredentialDetails(q url.Values, configurations ...string) {
	var details []map[string]any
	for _, c := range configurations {
		details = append(details, map[string]any{"type": "openid_credential", "credential_configuration_id": c})
	}
	raw, _ := json.Marshal(details)
	q.Set(authorization.AuthorizationDetailsClaim, string(raw))
}

// A-1 to A-3 (OpenID4VCI 1.0 sections 12.2.4 and 15.1): the consent for
// issuance names the credential and its claims, from the issuer's metadata.
func TestTheConsentForIssuanceNamesTheCredential(t *testing.T) {
	as := newTestAS(t)
	as.credentials = map[string]authorization.CredentialDisplay{
		issuanceResource + " belastingdienst-inkomensverklaring_2025": {Name: "Inkomensverklaring 2025", Claims: []string{"Jaar", "Verzamelinkomen"}},
	}
	issuer := as.registerIssuer(t, issuerRegistryURI, issuerOrganizationID)
	params := issuanceParams(newPKCE(), issuer, issuanceResource)
	setCredentialDetails(params, "belastingdienst-inkomensverklaring_2025")
	tx := as.startAuthorization(t, params)
	for _, want := range []string{"Inkomensverklaring 2025", "<li>Jaar</li>", "<li>Verzamelinkomen</li>"} {
		if !strings.Contains(tx.page, want) {
			t.Errorf("A-1: request page lacks %q:\n%s", want, tx.page)
		}
	}
	if strings.Contains(tx.page, "Een attestatie voor uw wallet") {
		t.Error("A-1: the page shows the generic text next to the credential")
	}

	t.Run("A-2: a configuration the issuer does not have", func(t *testing.T) {
		params := issuanceParams(newPKCE(), issuer, issuanceResource)
		setCredentialDetails(params, "belastingdienst-nope")
		assertIssuerAuthorizeError(t, as.authorize(t, params), "invalid_authorization_details")
	})
	t.Run("A-2: not a JSON array of details", func(t *testing.T) {
		params := issuanceParams(newPKCE(), issuer, issuanceResource)
		params.Set(authorization.AuthorizationDetailsClaim, `{"type":"openid_credential"}`)
		assertIssuerAuthorizeError(t, as.authorize(t, params), "invalid_authorization_details")
	})
	t.Run("A-3: the metadata is unreachable", func(t *testing.T) {
		as.credentials = nil
		params := issuanceParams(newPKCE(), issuer, issuanceResource)
		setCredentialDetails(params, "belastingdienst-inkomensverklaring_2025")
		tx := as.startAuthorization(t, params)
		if !strings.Contains(tx.page, "Een attestatie voor uw wallet") {
			t.Errorf("request page without metadata lacks the generic text:\n%s", tx.page)
		}
	})
}
