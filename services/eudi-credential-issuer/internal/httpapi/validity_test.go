package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"gbo-demo/oid4vc-kit/oid4vci"
)

// A credential without a revocation reference cannot be revoked by the
// provider, so it is short-lived (ARF VCR_01b): valid for 24 hours from the
// start of the hour it was issued. One with a reference is valid from the
// start of the day for the configuration's valid_days (365 in the fixture).

// wantShortLived requires claims of a credential valid for 24 hours from
// the start of the hour, now included.
func wantShortLived(t *testing.T, name string, claims map[string]any) {
	t.Helper()
	nbf, okNbf := claims["nbf"].(float64)
	exp, okExp := claims["exp"].(float64)
	now := float64(time.Now().Unix())
	if !okNbf || !okExp || int64(nbf)%3600 != 0 || exp-nbf != 24*3600 || nbf > now || now >= exp {
		t.Errorf("%s: nbf %v, exp %v: want 24 hours from the start of the hour", name, claims["nbf"], claims["exp"])
	}
}

// wantLongLived requires claims of a credential valid for 365 days from the
// start of the day.
func wantLongLived(t *testing.T, name string, claims map[string]any) {
	t.Helper()
	nbf, okNbf := claims["nbf"].(float64)
	exp, okExp := claims["exp"].(float64)
	if !okNbf || !okExp || int64(nbf)%86400 != 0 || exp-nbf != 365*86400 {
		t.Errorf("%s: nbf %v, exp %v: want 365 days from the start of the day", name, claims["nbf"], claims["exp"])
	}
}

func firstCredential(t *testing.T, name string, status int, body map[string]any) string {
	t.Helper()
	creds := credentialsOf(body)
	if status != http.StatusOK || len(creds) == 0 {
		t.Fatalf("%s: credential = %d %v", name, status, body)
	}
	return creds[0]
}

// T2: an offer with a reference keeps its configured validity, on every
// profile.
func TestValidityWithAReference(t *testing.T) {
	f := newFixture(t)
	for _, p := range credentialProfiles {
		w := f.walletOn(p.profile, p.as)
		tok := w.token(f.preAuthCodeRef(p.profile, "case-validity-"+p.name))
		c := &credentialCase{f: f, w: w, as: p.as, at: tok.AccessToken, credID: tok.AuthorizationDetails[0].CredentialIdentifiers[0]}
		wantLongLived(t, p.name, parseCredential(t, f, credentialsOf(c.issue(t))[0]))
	}
}

// T3: the authorization code flow at a source provider has no reference.
func TestValidityAfterALogin(t *testing.T) {
	f := newFixture(t)
	w := f.acfWallet()
	status, body := w.tokenForCode(w.code(nil), nil, nil)
	if status != http.StatusOK {
		t.Fatalf("token = %d %v", status, body)
	}
	var tok oid4vci.TokenResponse
	remarshal(t, body, &tok)
	status, cred := w.issueWith(t, tok)
	wantShortLived(t, "login", parseCredential(t, f, firstCredential(t, "login", status, cred)))
}

// T4: disclosure-based issuance has no reference, on both profiles.
func TestValidityAfterADisclosure(t *testing.T) {
	for _, profile := range []string{"nl", "eudi"} {
		f := newFixture(t)
		_, body := f.disclose(t, profile, "bsn")
		u, _ := url.Parse(body["redirect_uri"].(string))
		var offer oid4vci.CredentialOffer
		if err := json.Unmarshal([]byte(u.Query().Get("credential_offer")), &offer); err != nil {
			t.Fatal(err)
		}
		w := f.wallet(profile)
		tok := w.token(offer.Grants[oid4vci.GrantPreAuthorizedCode].(map[string]any)["pre-authorized_code"].(string))
		c := &credentialCase{f: f, w: w, as: profile, at: tok.AccessToken, credID: tok.AuthorizationDetails[0].CredentialIdentifiers[0]}
		wantShortLived(t, "disclosure "+profile, parseCredential(t, f, credentialsOf(c.issue(t))[0]))
	}
}

// T5: a deferred credential without a reference is short-lived too; its
// validity starts when it is issued.
func TestValidityOfADeferredCredential(t *testing.T) {
	f := newFixture(t)
	w, tok, _, cred := deferredStart(t, f)
	f.setSource(http.StatusOK, f.sourceDocs)
	status, body := w.deferred(tok.AccessToken, nil, map[string]any{"transaction_id": cred["transaction_id"]})
	wantShortLived(t, "deferred", parseCredential(t, f, firstCredential(t, "deferred", status, body)))
}
