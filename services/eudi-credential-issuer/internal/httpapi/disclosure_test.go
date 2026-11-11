package httpapi_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

const (
	incomeOffer   = "income-2025"
	pidStatusURI  = "https://pid.example.test/status/pid"
	pidIndex      = 1
	pidBSN        = "999991772"
	disclosureTTL = 2 * time.Minute
)

// verifierRegistration is a WRPRC for the verifier role that authorizes
// the bsn of the PID.
func verifierRegistration(t *testing.T, sub string) []byte {
	t.Helper()
	registrar := testpki.NewRoot(t, "registrar root").Issue(t, testpki.Options{})
	return testwallet.VerifierRegistrationCertificate(t, registrar, sub, issuance.PIDType, []string{"bsn"})
}

func (f *fixture) fetchPIDStatus(_ context.Context, uri string) (string, error) {
	f.sourceMu.Lock()
	defer f.sourceMu.Unlock()
	if f.pidStatusDown || uri != pidStatusURI {
		return "", errors.New("unreachable")
	}
	return statuslist.Sign(statuslist.Token{URI: uri, IssuedAt: time.Now(), Expires: time.Now().Add(time.Hour), Size: 16, Revoked: f.pidRevoked},
		f.pidStatus.Key, x5c.Header(f.pidStatus.Chain))
}

// pid issues a test PID bound to holder, all disclosures included.
func (f *fixture) pid(t *testing.T, issuer testpki.Cert, holder *ecdsa.PrivateKey) string {
	t.Helper()
	s, err := sdjwtvc.NewSigner(issuer.Key, x5c.Header(issuer.Chain))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	cred, err := s.Issue(sdjwtvc.Credential{
		VCT: issuance.PIDType, IssuedAt: now, NotBefore: now.Add(-time.Hour), Expires: now.AddDate(1, 0, 0),
		HolderKey: &jose.JSONWebKey{Key: &holder.PublicKey}, Status: &sdjwtvc.StatusReference{Index: pidIndex, URI: pidStatusURI},
		Claims: map[string]any{"bsn": pidBSN, "given_name": "Frouke"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

// vpRequest is a fetched, verified request object.
type vpRequest struct {
	claims map[string]any
}

func (r vpRequest) str(name string) string { s, _ := r.claims[name].(string); return s }

// fetchRequest fetches the request object of offer under profile as a
// wallet does, and verifies it against the tax provider's access root.
func (f *fixture) fetchRequest(t *testing.T, provider, profile, offer string) (int, vpRequest) {
	t.Helper()
	status, body, header := get(t, f.idOf(provider, profile)+"/vp/"+offer+"/request", "")
	if status != http.StatusOK {
		return status, vpRequest{}
	}
	if ct := header.Get("Content-Type"); ct != "application/oauth-authz-req+jwt" {
		t.Fatalf("content type %q", ct)
	}
	tok, err := jwt.ParseSigned(body, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	leaf, _, err := x5c.Verify(tok.Headers[0], []x5c.Anchors{x5c.NewAnchors("access", f.accessRoot.Cert)}, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := tok.Claims(leaf.PublicKey, &claims); err != nil {
		t.Fatal(err)
	}
	return status, vpRequest{claims: claims}
}

// presentation discloses keep from a PID of issuer, bound to the request.
func (f *fixture) presentation(t *testing.T, r vpRequest, issuer testpki.Cert, keep ...string) url.Values {
	t.Helper()
	holder := testpki.NewKey(t)
	pres := testwallet.Present(t, testwallet.Select(t, f.pid(t, issuer, holder), keep...), holder, r.str("client_id"), r.str("nonce"), f.clock.Now())
	return f.encryptResponse(t, r, map[string]any{"vp_token": map[string]any{"pid": []string{pres}}, "state": r.str("state")})
}

func (f *fixture) encryptResponse(t *testing.T, r vpRequest, body any) url.Values {
	t.Helper()
	keys := r.claims["client_metadata"].(map[string]any)["jwks"].(map[string]any)["keys"].([]any)
	var key jose.JSONWebKey
	remarshal(t, keys[0], &key)
	raw, _ := json.Marshal(body)
	return url.Values{"response": {testwallet.EncryptRequest(t, raw, key, jose.A256GCM, nil)}}
}

// respond posts form to the request's response_uri.
func (f *fixture) respond(t *testing.T, r vpRequest, form url.Values) (int, map[string]any) {
	t.Helper()
	return f.respondTo(t, r.str("response_uri"), form)
}

func (f *fixture) respondTo(t *testing.T, responseURI string, form url.Values) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(responseURI, form)
	if err != nil {
		t.Fatal(err)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
	return decode(t, resp)
}

// disclose runs a disclosure for profile up to the response.
func (f *fixture) disclose(t *testing.T, profile string, keep ...string) (int, map[string]any) {
	t.Helper()
	status, r := f.fetchRequest(t, "tax", profile, incomeOffer)
	if status != http.StatusOK {
		t.Fatalf("request: %d", status)
	}
	return f.respond(t, r, f.presentation(t, r, f.pidIssuer, keep...))
}

// D1 and D2: the request object of an offer, per profile.
func TestDisclosureRequest(t *testing.T) {
	f := newFixture(t)
	for _, profile := range []string{"nl", "eudi"} {
		status, r := f.fetchRequest(t, "tax", profile, incomeOffer)
		if status != http.StatusOK {
			t.Fatalf("%s: %d", profile, status)
		}
		if !strings.HasPrefix(r.str("client_id"), "x509_hash:") || r.str("response_mode") != "direct_post.jwt" ||
			!strings.HasPrefix(r.str("response_uri"), f.id(profile)+"/vp/response/") {
			t.Fatalf("%s: %v", profile, r.claims)
		}
		data := r.claims["verifier_info"].([]any)[0].(map[string]any)["data"].(string)
		jwtForm := strings.Count(data, ".") == 2
		if (profile == "nl") == jwtForm {
			t.Errorf("%s: verifier_info data %q must be base64url for nl and the JWT for eudi", profile, data)
		}
		if profile == "nl" {
			if raw, err := base64.RawURLEncoding.DecodeString(data); err != nil || strings.Count(string(raw), ".") != 2 {
				t.Errorf("nl: verifier_info data does not decode to a JWT")
			}
		}
		claim := r.claims["dcql_query"].(map[string]any)["credentials"].([]any)[0].(map[string]any)["claims"].([]any)[0]
		if path := claim.(map[string]any)["path"].([]any); len(path) != 1 || path[0] != "bsn" {
			t.Errorf("%s: dcql asks for %v", profile, path)
		}
		_, again := f.fetchRequest(t, "tax", profile, incomeOffer)
		if again.str("state") == r.str("state") || again.str("nonce") == r.str("nonce") || again.str("response_uri") == r.str("response_uri") {
			t.Errorf("%s: each fetch must start a new session", profile)
		}
	}
	for name, path := range map[string][3]string{
		"unknown offer":           {"tax", "nl", "other"},
		"unknown provider":        {"nope", "nl", incomeOffer},
		"unknown profile":         {"tax", "nope", incomeOffer},
		"offer of other provider": {"civil", "nl", incomeOffer},
	} {
		if status, _ := f.fetchRequest(t, path[0], path[1], path[2]); status != http.StatusNotFound {
			t.Errorf("D2 %s: %d", name, status)
		}
	}
}

// D3 and D10: a disclosed BSN becomes an offer the wallet of the profile
// redeems.
func TestDisclosureIssuance(t *testing.T) {
	for _, profile := range []string{"nl", "eudi"} {
		t.Run(profile, func(t *testing.T) {
			f := newFixture(t)
			status, body := f.disclose(t, profile, "bsn")
			redirect, _ := body["redirect_uri"].(string)
			if status != http.StatusOK || !strings.HasPrefix(redirect, "openid-credential-offer://?credential_offer=") {
				t.Fatalf("D3: %d %v", status, body)
			}
			u, _ := url.Parse(redirect)
			var offer oid4vci.CredentialOffer
			if err := json.Unmarshal([]byte(u.Query().Get("credential_offer")), &offer); err != nil {
				t.Fatal(err)
			}
			if offer.CredentialIssuer != f.id(profile) || len(offer.CredentialConfigurationIDs) != 1 || offer.CredentialConfigurationIDs[0] != incomeConfig {
				t.Fatalf("offer %+v", offer)
			}
			grant := offer.Grants[oid4vci.GrantPreAuthorizedCode].(map[string]any)
			if _, ok := grant["tx_code"]; ok {
				t.Fatal("the offer must have no tx_code")
			}
			checkDisclosureSent(t, f)

			// D10: the offer redeems at the profile's token and credential
			// endpoints.
			w := f.wallet(profile)
			tok := w.token(grant["pre-authorized_code"].(string))
			proofs := &oid4vci.Proofs{JWT: []string{testwallet.JWTProof(t, testpki.NewKey(t), f.id("nl"), w.nonce(), "")}}
			if profile == "eudi" {
				ka := f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, w.nonce(), time.Now().Add(time.Hour))
				proofs = &oid4vci.Proofs{Attestation: []string{ka}}
			}
			if status, body := w.credentialRequest(tok.AccessToken, w.dpopKey, oid4vci.CredentialRequest{
				CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0], Proofs: proofs,
			}); status != http.StatusOK {
				t.Fatalf("D10: %d %v", status, body)
			}
		})
	}
	t.Run("the offer expires with the disclosure lifetime", func(t *testing.T) {
		f := newFixture(t)
		// The disclosure was made just over its lifetime ago.
		f.clock.Advance(-disclosureTTL - time.Second)
		_, body := f.disclose(t, "nl", "bsn")
		f.clock.Advance(disclosureTTL + time.Second)
		u, _ := url.Parse(body["redirect_uri"].(string))
		var offer oid4vci.CredentialOffer
		_ = json.Unmarshal([]byte(u.Query().Get("credential_offer")), &offer)
		code := offer.Grants[oid4vci.GrantPreAuthorizedCode].(map[string]any)["pre-authorized_code"].(string)
		if status, body := f.wallet("nl").tokenRequest(code, nil); status != http.StatusBadRequest || body["error"] != oid4vci.ErrInvalidGrant {
			t.Fatalf("%d %v", status, body)
		}
	})
}

// checkDisclosureSent requires the last source call to carry the disclosed
// BSN in the form of nl-wallet's issuance_server, with the offer's
// parameters.
func checkDisclosureSent(t *testing.T, f *fixture) {
	t.Helper()
	var sent []struct {
		ID           string `json:"id"`
		Attestations []struct {
			AttestationType  string         `json:"attestation_type"`
			Format           string         `json:"format"`
			Attributes       map[string]any `json:"attributes"`
			CA               string         `json:"ca"`
			IssuanceValidity map[string]any `json:"issuance_validity"`
			RevocationStatus string         `json:"revocation_status"`
		} `json:"attestations"`
	}
	if err := json.Unmarshal(f.lastSourceBody(), &sent); err != nil || len(sent) != 1 || len(sent[0].Attestations) != 1 {
		t.Fatalf("source got %s", f.lastSourceBody())
	}
	a := sent[0].Attestations[0]
	if sent[0].ID != "pid" || a.AttestationType != issuance.PIDType || a.Format != "dc+sd-jwt" || a.CA != "pid root" ||
		a.RevocationStatus != "Valid" || len(a.Attributes) != 1 || a.Attributes["bsn"] != pidBSN ||
		a.IssuanceValidity["signed"] == nil || a.IssuanceValidity["validUntil"] == nil {
		t.Fatalf("source got %s", f.lastSourceBody())
	}
	if q := f.lastSourceCall(); q.Get("jaar") != "2025" {
		t.Fatalf("parameters %v", q)
	}
}

// D4 to D9: what the wallet gets when the disclosure does not lead to an
// offer.
func TestDisclosureRefusals(t *testing.T) {
	expect := func(t *testing.T, name string, status int, body map[string]any, wantStatus int, wantError string) {
		t.Helper()
		if status != wantStatus || body["error"] != wantError {
			t.Errorf("%s: %d %v, want %d %s", name, status, body, wantStatus, wantError)
		}
	}
	// R2: the NL Wallet shows no_issuable_attestations as "nothing to add";
	// the eudi profile redirects to a page instead (TestDisclosureNoDataPage).
	t.Run("D4 no data", func(t *testing.T) {
		f := newFixture(t)
		for _, profile := range []string{"nl"} {
			f.setSource(http.StatusNotFound, nil)
			status, body := f.disclose(t, profile, "bsn")
			expect(t, profile+" 404", status, body, http.StatusNotFound, "no_issuable_attestations")
			f.setSource(http.StatusOK, []map[string]any{})
			status, body = f.disclose(t, profile, "bsn")
			expect(t, profile+" empty list", status, body, http.StatusNotFound, "no_issuable_attestations")
		}
	})
	t.Run("D5 source unavailable or unusable", func(t *testing.T) {
		f := newFixture(t)
		f.setSource(http.StatusBadGateway, nil)
		status, body := f.disclose(t, "nl", "bsn")
		expect(t, "unavailable", status, body, http.StatusInternalServerError, "server_error")
		f.setSource(http.StatusOK, []map[string]any{{"format": "dc+sd-jwt", "attestation_type": "other", "attributes": map[string]any{"year": "2025"}}})
		status, body = f.disclose(t, "nl", "bsn")
		expect(t, "unusable data", status, body, http.StatusInternalServerError, "server_error")
	})
	t.Run("D6 refused by the verifier", func(t *testing.T) {
		f := newFixture(t)
		_, r := f.fetchRequest(t, "tax", "nl", incomeOffer)
		status, body := f.respond(t, r, f.presentation(t, r, f.pidIssuer, "bsn", "given_name"))
		expect(t, "extra claim", status, body, http.StatusBadRequest, "invalid_request")
		status, body = f.respond(t, r, f.presentation(t, r, f.pidIssuer, "bsn"))
		expect(t, "a valid response after a refusal", status, body, http.StatusBadRequest, "invalid_request")
		f.pidRevoked = []int{pidIndex}
		status, body = f.disclose(t, "nl", "bsn")
		expect(t, "revoked", status, body, http.StatusBadRequest, "invalid_request")
		f.pidRevoked = nil
		untrusted := testpki.NewRoot(t, "untrusted").Issue(t, testpki.Options{EKU: f.pidIssuer.Cert.UnknownExtKeyUsage})
		_, r = f.fetchRequest(t, "tax", "nl", incomeOffer)
		status, body = f.respond(t, r, f.presentation(t, r, untrusted, "bsn"))
		expect(t, "untrusted PID", status, body, http.StatusBadRequest, "invalid_request")
		_, r = f.fetchRequest(t, "tax", "nl", incomeOffer)
		status, body = f.respond(t, r, url.Values{})
		expect(t, "no response", status, body, http.StatusBadRequest, "invalid_request")
		if n := f.sourceCallCount(); n != 0 {
			t.Errorf("the source was called %d times", n)
		}
		if strings.Contains(f.logs.String(), pidBSN) {
			t.Error("the BSN must not be logged")
		}
	})
	t.Run("D7 PID status list unreachable", func(t *testing.T) {
		f := newFixture(t)
		f.pidStatusDown = true
		status, body := f.disclose(t, "eudi", "bsn")
		expect(t, "status down", status, body, http.StatusInternalServerError, "server_error")
	})
	t.Run("D8 unknown, expired or used session", func(t *testing.T) {
		f := newFixture(t)
		_, r := f.fetchRequest(t, "tax", "nl", incomeOffer)
		form := f.presentation(t, r, f.pidIssuer, "bsn")
		if status, body := f.respond(t, r, form); status != http.StatusOK {
			t.Fatalf("first response: %d %v", status, body)
		}
		status, body := f.respond(t, r, form)
		expect(t, "used", status, body, http.StatusBadRequest, "invalid_request")
		status, body = f.respondTo(t, f.id("nl")+"/vp/response/unknown", form)
		expect(t, "unknown", status, body, http.StatusBadRequest, "invalid_request")
		_, r = f.fetchRequest(t, "tax", "nl", incomeOffer)
		form = f.presentation(t, r, f.pidIssuer, "bsn")
		status, body = f.respondTo(t, strings.Replace(r.str("response_uri"), "/nl/", "/eudi/", 1), form)
		expect(t, "other profile", status, body, http.StatusBadRequest, "invalid_request")
		status, body = f.respond(t, r, form)
		expect(t, "the right profile after the wrong one", status, body, http.StatusBadRequest, "invalid_request")
		_, r = f.fetchRequest(t, "tax", "nl", incomeOffer)
		status, body = f.respondTo(t, strings.Replace(r.str("response_uri"), "/tax/", "/civil/", 1), f.presentation(t, r, f.pidIssuer, "bsn"))
		expect(t, "other provider", status, body, http.StatusBadRequest, "invalid_request")
		_, r = f.fetchRequest(t, "tax", "nl", incomeOffer)
		form = f.presentation(t, r, f.pidIssuer, "bsn")
		f.clock.Advance(disclosureTTL + time.Second)
		status, body = f.respond(t, r, form)
		expect(t, "expired", status, body, http.StatusBadRequest, "invalid_request")
	})
	t.Run("D9 the wallet posts an error", func(t *testing.T) {
		f := newFixture(t)
		_, r := f.fetchRequest(t, "tax", "eudi", incomeOffer)
		status, body := f.respond(t, r, url.Values{"error": {"access_denied"}, "state": {"other"}})
		expect(t, "an error with another state", status, body, http.StatusBadRequest, "invalid_request")
		_, r = f.fetchRequest(t, "tax", "eudi", incomeOffer)
		status, body = f.respond(t, r, url.Values{"error": {"access_denied"}, "state": {r.str("state")}})
		if status != http.StatusOK || len(body) != 0 {
			t.Fatalf("%d %v", status, body)
		}
		status, body = f.respond(t, r, f.presentation(t, r, f.pidIssuer, "bsn"))
		expect(t, "after the error", status, body, http.StatusBadRequest, "invalid_request")
		if n := f.sourceCallCount(); n != 0 {
			t.Errorf("the source was called %d times", n)
		}
	})
}

// F1: the fixed links of the disclosure offers, public and per profile.
func TestDisclosureOffers(t *testing.T) {
	f := newFixture(t)
	status, body, header := get(t, f.public.URL+"/disclosure-offers", "")
	if status != http.StatusOK || header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%d %v", status, header)
	}
	var offers []issuance.DisclosureOfferLinks
	if err := json.Unmarshal([]byte(body), &offers); err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].Provider != "tax" || offers[0].Key != incomeOffer || len(offers[0].Profiles) != 2 {
		t.Fatalf("offers %+v", offers)
	}
	for profile, prefix := range map[string]string{
		"nl":   "https://app.example.test/deeplink/disclosure_based_issuance?",
		"eudi": "openid4vp://?",
	} {
		l := offers[0].Profiles[profile]
		_, r := f.fetchRequest(t, "tax", profile, incomeOffer)
		if l.ClientID != r.str("client_id") || l.RequestURI != f.id(profile)+"/vp/"+incomeOffer+"/request" || !strings.HasPrefix(l.Link, prefix) {
			t.Errorf("%s: %+v", profile, l)
		}
		u, err := url.Parse(l.Link)
		if err != nil || u.Query().Get("client_id") != l.ClientID || u.Query().Get("request_uri") != l.RequestURI {
			t.Errorf("%s: link %s", profile, l.Link)
		}
	}
}

// R1 and R3 to R5: the EUDI wallets show a refusal as a generic error,
// so the eudi profile sends the citizen to a page that says the source has
// no data.
func TestDisclosureNoDataPage(t *testing.T) {
	page := func(f *fixture) string { return f.id("eudi") + "/vp/" + incomeOffer + "/no-data" }
	t.Run("R1 eudi redirects to the page", func(t *testing.T) {
		f := newFixture(t)
		for name, docs := range map[string][]map[string]any{"404": nil, "empty list": {}} {
			if docs == nil {
				f.setSource(http.StatusNotFound, nil)
			} else {
				f.setSource(http.StatusOK, docs)
			}
			_, r := f.fetchRequest(t, "tax", "eudi", incomeOffer)
			form := f.presentation(t, r, f.pidIssuer, "bsn")
			status, body := f.respond(t, r, form)
			if status != http.StatusOK || body["redirect_uri"] != page(f) || len(body) != 1 {
				t.Errorf("%s: %d %v, want 200 with redirect_uri %s", name, status, body, page(f))
			}
			if status, body := f.respond(t, r, form); status != http.StatusBadRequest || body["error"] != "invalid_request" {
				t.Errorf("%s: second response %d %v, want the session ended", name, status, body)
			}
		}
	})
	t.Run("R3 the page", func(t *testing.T) {
		f := newFixture(t)
		status, body, header := get(t, page(f), "")
		if status != http.StatusOK || !strings.HasPrefix(header.Get("Content-Type"), "text/html") || header.Get("Cache-Control") != "no-store" {
			t.Fatalf("page: %d %v", status, header)
		}
		for _, text := range []string{"Geen gegevens gevonden", "tax", "Income statement", "Er is niets aan uw wallet toegevoegd"} {
			if !strings.Contains(body, text) {
				t.Errorf("page does not contain %q", text)
			}
		}
		if strings.Contains(body, pidBSN) {
			t.Error("the page must not show the BSN")
		}
	})
	t.Run("R4 no page for an unknown offer", func(t *testing.T) {
		f := newFixture(t)
		for name, url := range map[string]string{
			"unknown offer":           f.id("eudi") + "/vp/other/no-data",
			"unknown provider":        f.idOf("nope", "eudi") + "/vp/" + incomeOffer + "/no-data",
			"unknown profile":         f.idOf("tax", "nope") + "/vp/" + incomeOffer + "/no-data",
			"nl profile":              f.id("nl") + "/vp/" + incomeOffer + "/no-data",
			"offer of other provider": f.idOf("civil", "eudi") + "/vp/" + incomeOffer + "/no-data",
		} {
			if status, _, _ := get(t, url, ""); status != http.StatusNotFound {
				t.Errorf("%s: %d, want 404", name, status)
			}
		}
	})
	t.Run("R5 other failures stay a server error", func(t *testing.T) {
		f := newFixture(t)
		f.setSource(http.StatusBadGateway, nil)
		if status, body := f.disclose(t, "eudi", "bsn"); status != http.StatusInternalServerError || body["error"] != "server_error" {
			t.Errorf("unavailable: %d %v", status, body)
		}
		f.setSource(http.StatusOK, []map[string]any{{"format": "dc+sd-jwt", "attestation_type": "other", "attributes": map[string]any{"year": "2025"}}})
		if status, body := f.disclose(t, "eudi", "bsn"); status != http.StatusInternalServerError || body["error"] != "server_error" {
			t.Errorf("unusable data: %d %v", status, body)
		}
	})
}

// B-1, B-2 (HAIP 1.0 section 5): the PID query carries an aki-based
// trusted_authorities query when the profile configures one, and none
// otherwise.
func TestDisclosureTrustedAuthorities(t *testing.T) {
	aki := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdefghij"))
	f := newFixtureWith(t, func(_ *fixture, cfg *issuance.Config) {
		for i := range cfg.Profiles {
			if cfg.Profiles[i].Name == "eudi" {
				cfg.Profiles[i].PIDTrustedAuthorities = []string{aki}
			}
		}
	})
	query := func(profile string) map[string]any {
		_, r := f.fetchRequest(t, "tax", profile, incomeOffer)
		return r.claims["dcql_query"].(map[string]any)["credentials"].([]any)[0].(map[string]any)
	}
	ta, _ := query("eudi")["trusted_authorities"].([]any)
	if len(ta) != 1 {
		t.Fatalf("B-1: eudi trusted_authorities = %v, want one aki query", query("eudi")["trusted_authorities"])
	}
	if q := ta[0].(map[string]any); q["type"] != "aki" || len(q["values"].([]any)) != 1 || q["values"].([]any)[0] != aki {
		t.Errorf("B-1: trusted_authorities = %v", q)
	}
	if _, ok := query("nl")["trusted_authorities"]; ok {
		t.Errorf("B-2: nl, without configuration, has trusted_authorities")
	}
}
