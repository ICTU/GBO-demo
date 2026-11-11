package httpapi_test

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/eudi-credential-issuer/internal/testlogin"
	"gbo-demo/eudi-credential-issuer/internal/testloginhttp"
	"gbo-demo/eudi-credential-issuer/internal/testpersons"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/x5c"
)

const testPIDConfig = "test-pid-pid"

func personFixture(t *testing.T) (*fixture, *acfWallet, string, testpki.Cert) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "persons.json")
	if err := os.WriteFile(file, []byte(`[{"bsn":"999991772","name":"Frouke Jansen"},{"bsn":"999993653","name":"Andere persoon"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var root testpki.Cert
	f := newFixtureWith(t, func(f *fixture, cfg *issuance.Config) {
		p, r, _ := newProvider(t, "test-pid", issuance.PIDType)
		root = r
		selector := testlogin.New(p.Name, f.idOf(p.Name, "eudi"), testpersons.File(file), memstore.New(), f.clock.Now)
		p.Login = selector
		cfg.Providers = append(cfg.Providers, p)
		cfg.Configurations = append(cfg.Configurations, issuance.CredentialConfiguration{ID: testPIDConfig, Provider: p.Name, Scope: testPIDConfig, VCT: issuance.PIDType, ValidDays: 365, StatusList: "pid", Display: []issuance.Display{{Name: "GBO test-PID", Locale: "nl"}}, Claims: []issuance.Claim{{Path: []string{"bsn"}, Mandatory: true}, {Path: []string{"given_name"}}}})
		cfg.LoginOffers = []issuance.DisclosureOffer{{Key: "test-pid", Provider: p.Name, ConfigurationIDs: []string{testPIDConfig}}}
		f.browserRoutes = map[string]http.Handler{selector.Path(): testloginhttp.Handler(selector, walletRedirect)}
	})
	w := f.acfWallet()
	w.issuer = "test-pid"
	return f, w, file, root
}
func personForm(w *acfWallet) url.Values {
	form := w.parForm()
	form.Del("issuer_state")
	form.Set("scope", testPIDConfig)
	return form
}
func personPage(t *testing.T, w *acfWallet) (string, string) {
	t.Helper()
	status, body := w.par(personForm(w), nil)
	if status != 201 {
		t.Fatalf("PAR: %d %v", status, body)
	}
	status, location, raw := browse(t, w.f.idOf("test-pid", "eudi")+"/authorize?"+url.Values{"client_id": {w.clientID}, "request_uri": {body["request_uri"].(string)}}.Encode())
	if status != 302 || !strings.HasPrefix(location, w.f.idOf("test-pid", "eudi")+"/test-persons?") {
		t.Fatalf("local authorize: %d %s %s", status, location, raw)
	}
	status, _, raw = browse(t, location)
	if status != 200 {
		t.Fatalf("picker: %d %s", status, raw)
	}
	return location, raw
}
func choosePerson(t *testing.T, page, bsn string) (int, string) {
	t.Helper()
	u, _ := url.Parse(page)
	resp, err := noRedirect.PostForm(strings.Split(page, "?")[0], url.Values{"ticket": {u.Query().Get("ticket")}, "bsn": {bsn}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location")
}
func personCode(t *testing.T, w *acfWallet, bsn string) string {
	t.Helper()
	page, _ := personPage(t, w)
	status, callback := choosePerson(t, page, bsn)
	if status != 303 {
		t.Fatalf("choose: %d", status)
	}
	status, location, raw := browse(t, callback)
	if status != 302 {
		t.Fatalf("callback: %d %s", status, raw)
	}
	u, _ := url.Parse(location)
	if u.Query().Get("state") != w.state {
		t.Fatal("wallet state lost")
	}
	return u.Query().Get("code")
}

func TestTestPersonIssuance(t *testing.T) { // T10, T11, T15
	for _, bsn := range []string{"999991772", "999993653"} {
		t.Run(bsn, func(t *testing.T) {
			f, w, _, root := personFixture(t)
			f.setSource(200, []map[string]any{{"format": "dc+sd-jwt", "attestation_type": issuance.PIDType, "attributes": map[string]any{"bsn": bsn, "given_name": "Chosen"}}})
			page, html := personPage(t, w)
			if !strings.Contains(html, "Frouke Jansen") || !strings.Contains(html, "Andere persoon") || !strings.Contains(html, "fictieve personen") {
				t.Fatal("person choices or demo notice missing")
			}
			status, callback := choosePerson(t, page, bsn)
			if status != 303 {
				t.Fatal(status)
			}
			status, location, body := browse(t, callback)
			if status != 302 {
				t.Fatalf("callback: %d %s", status, body)
			}
			u, _ := url.Parse(location)
			if u.Query().Get("state") != w.state || u.Query().Get("code") == "" {
				t.Fatal("wallet response not bound")
			}
			var source map[string]any
			if err := json.Unmarshal(f.lastSourceBody(), &source); err != nil {
				t.Fatal(err)
			}
			if source["subject"].(map[string]any)["bsn"] != bsn || source["origin"] != "login" {
				t.Fatal("wrong chosen subject")
			}
			status, tokens := w.tokenForCode(u.Query().Get("code"), nil, nil)
			if status != 200 {
				t.Fatalf("token: %d %v", status, tokens)
			}
			var token oid4vci.TokenResponse
			remarshal(t, tokens, &token)
			holder := testpki.NewKey(t)
			ka := f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{holder}, w.nonce(), time.Now().Add(time.Hour))
			status, cred := w.credentialRequest(token.AccessToken, w.dpopKey, oid4vci.CredentialRequest{CredentialIdentifier: token.AuthorizationDetails[0].CredentialIdentifiers[0], Proofs: &oid4vci.Proofs{Attestation: []string{ka}}})
			if status != 200 {
				t.Fatalf("credential: %d %v", status, cred)
			}
			raw := cred["credentials"].([]any)[0].(map[string]any)["credential"].(string)
			parsed, err := sdjwtvc.Parse(raw, []x5c.Anchors{x5c.NewAnchors("pid", root.Cert)}, mdlIssuerEKU, time.Now())
			if err != nil || parsed.Claims["vct"] != issuance.PIDType || parsed.Claims["bsn"] != bsn {
				t.Fatalf("PID: %v %v", parsed.Claims, err)
			}
			// A normal source still uses the authorization server.
			normal := f.acfWallet()
			if location := normal.authorize(normal.requestURI(nil)); !strings.HasPrefix(location, loginServer) {
				t.Fatal("other provider changed")
			}
			if strings.Contains(f.logs.String(), bsn) {
				t.Fatal("chosen BSN logged")
			}
		})
	}
}
func TestTestPersonRefusals(t *testing.T) {
	t.Run("T12 unknown person", func(t *testing.T) {
		_, w, _, _ := personFixture(t)
		page, _ := personPage(t, w)
		if status, _ := choosePerson(t, page, "123456789"); status != 400 {
			t.Fatal(status)
		}
		if status, _ := choosePerson(t, page, "999991772"); status != 303 {
			t.Fatal("invalid choice consumed request")
		}
	})
	t.Run("T13 reused code", func(t *testing.T) {
		f, w, _, _ := personFixture(t)
		f.setSource(200, []map[string]any{{"format": "dc+sd-jwt", "attestation_type": issuance.PIDType, "attributes": map[string]any{"bsn": loginBSN}}})
		code := personCode(t, w, loginBSN)
		if status, _ := w.tokenForCode(code, nil, nil); status != 200 {
			t.Fatal(status)
		}
		if status, body := w.tokenForCode(code, nil, nil); status != 400 || body["error"] != "invalid_grant" {
			t.Fatalf("%d %v", status, body)
		}
	})
	t.Run("T13 expired code", func(t *testing.T) {
		f, w, _, _ := personFixture(t)
		f.clock.Advance(-59 * time.Second)
		f.setSource(200, []map[string]any{{"format": "dc+sd-jwt", "attestation_type": issuance.PIDType, "attributes": map[string]any{"bsn": loginBSN}}})
		code := personCode(t, w, loginBSN)
		f.clock.Advance(61 * time.Second)
		if status, body := w.tokenForCode(code, nil, nil); status != 400 || body["error"] != "invalid_grant" {
			t.Fatalf("%d %v", status, body)
		}
	})
	t.Run("T14 wrong state", func(t *testing.T) {
		f, w, _, _ := personFixture(t)
		page, _ := personPage(t, w)
		_, callback := choosePerson(t, page, loginBSN)
		u, _ := url.Parse(callback)
		q := u.Query()
		q.Set("state", "wrong")
		u.RawQuery = q.Encode()
		if status, _, _ := browse(t, u.String()); status != 400 {
			t.Fatal(status)
		}
		if f.sourceCallCount() != 0 {
			t.Fatal("source called on wrong state")
		}
	})
	t.Run("T14 wrong PKCE", func(t *testing.T) {
		f, w, _, _ := personFixture(t)
		f.setSource(200, []map[string]any{{"format": "dc+sd-jwt", "attestation_type": issuance.PIDType, "attributes": map[string]any{"bsn": loginBSN}}})
		code := personCode(t, w, loginBSN)
		if status, body := w.tokenForCode(code, url.Values{"code_verifier": {strings.Repeat("x", 43)}}, nil); status != 400 || body["error"] != "invalid_grant" {
			t.Fatalf("%d %v", status, body)
		}
	})
	t.Run("T16 missing persons file", func(t *testing.T) {
		f, w, file, _ := personFixture(t)
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		status, par := w.par(personForm(w), nil)
		if status != 201 {
			t.Fatal(status)
		}
		status, _, body := browse(t, f.idOf("test-pid", "eudi")+"/authorize?"+url.Values{"client_id": {w.clientID}, "request_uri": {par["request_uri"].(string)}}.Encode())
		if status != 200 || !strings.Contains(body, "Inloggen niet beschikbaar") {
			t.Fatalf("%d %s", status, body)
		}
		f.setSource(200, []map[string]any{{"format": "dc+sd-jwt", "attestation_type": issuance.PIDType, "attributes": map[string]any{"bsn": loginBSN}}})
		resp, err := http.Post(f.internal.URL+"/offers", "application/json", strings.NewReader(`{"profile":"eudi","credential_configuration_ids":["test-pid-pid"],"subject":{"bsn":"999991772"}}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("pre-auth blocked: %d", resp.StatusCode)
		}
	})
}
