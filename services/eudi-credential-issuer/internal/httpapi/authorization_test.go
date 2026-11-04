package httpapi_test

// The authorization code flow with a login at GBO's authorization server
// (#614, phase 3, F4): PAR, the authorization request, the login callback,
// the token request with the code, and the pages a citizen sees when the
// source has no data or is unavailable (cases P1-P19, C5, C8-C11).

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

const (
	walletRedirect = "eu.europa.ec.euidi://authorization"
	loginServer    = "https://login.test/authorize"
	loginBSN       = "999991772"
	parPrefix      = "urn:ietf:params:oauth:request_uri:"
)

// fakeLogin stands in for GBO's authorization server: it hands out a login
// URL and, for the code it gets back, the BSN or an error.
type fakeLogin struct {
	mu         sync.Mutex
	bsn        string
	err        error
	urlErr     error
	challenges map[string]string // state -> challenge
	exchanges  []string          // provider/resource of each exchange
}

func newFakeLogin() *fakeLogin { return &fakeLogin{bsn: loginBSN, challenges: map[string]string{}} }

func (l *fakeLogin) AuthorizationURL(_ context.Context, provider, resource, state, challenge string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.urlErr != nil {
		return "", l.urlErr
	}
	l.challenges[state] = challenge
	return loginServer + "?" + url.Values{"provider": {provider}, "resource": {resource}, "state": {state}}.Encode(), nil
}

// Exchange accepts the code "login-code" when the verifier matches the
// challenge of the state it was issued for (the code carries the state).
func (l *fakeLogin) Exchange(_ context.Context, provider, resource, code, verifier string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.exchanges = append(l.exchanges, provider+" "+resource)
	if l.err != nil {
		return "", l.err
	}
	state, ok := strings.CutPrefix(code, "login-code-")
	if !ok {
		return "", errors.New("unknown code")
	}
	sum := sha256.Sum256([]byte(verifier))
	if l.challenges[state] != base64.RawURLEncoding.EncodeToString(sum[:]) {
		return "", errors.New("code_verifier does not match")
	}
	return l.bsn, nil
}

// noRedirect is a client that does not follow redirects.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// acfWallet is a wallet in the authorization code flow, with its PKCE pair.
type acfWallet struct {
	*wallet
	verifier, challenge string
	state               string
}

func (f *fixture) acfWallet() *acfWallet {
	w := &acfWallet{wallet: f.wallet("eudi"), state: "wallet-state"}
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i * 7)
	}
	w.verifier = base64.RawURLEncoding.EncodeToString(append(b, []byte(f.t.Name())...))
	sum := sha256.Sum256([]byte(w.verifier))
	w.challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return w
}

// parForm is a valid pushed authorization request for the income offer.
func (w *acfWallet) parForm() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {w.clientID},
		"redirect_uri":          {walletRedirect},
		"code_challenge":        {w.challenge},
		"code_challenge_method": {"S256"},
		"state":                 {w.state},
		"issuer_state":          {incomeOffer},
		"scope":                 {"income"},
	}
}

// par posts form to the PAR endpoint; headers override the client
// attestation and add a DPoP proof when given.
func (w *acfWallet) par(form url.Values, headers map[string]string) (int, map[string]any) {
	w.f.t.Helper()
	id := w.f.idOf(w.issuer, w.profile)
	h := map[string]string{
		"OAuth-Client-Attestation":     w.wia,
		"OAuth-Client-Attestation-PoP": testwallet.PoP(w.f.t, w.wiaKey, w.clientID, id, w.challengeValue()),
	}
	for k, v := range headers {
		if v == "" {
			delete(h, k)
		} else {
			h[k] = v
		}
	}
	return w.post("/par", strings.NewReader(form.Encode()), h, "Content-Type", "application/x-www-form-urlencoded")
}

func (w *acfWallet) challengeValue() string { return w.wallet.challenge() }

// requestURI pushes a valid request and returns its request_uri.
func (w *acfWallet) requestURI(headers map[string]string) string {
	w.f.t.Helper()
	status, body := w.par(w.parForm(), headers)
	if status != http.StatusCreated {
		w.f.t.Fatalf("PAR: %d %v", status, body)
	}
	return body["request_uri"].(string)
}

// browse fetches u without following redirects and returns the status, the
// Location and the body.
func browse(t *testing.T, u string) (int, string, string) {
	t.Helper()
	resp, err := noRedirect.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Location"), string(raw)
}

// authorize opens the authorization endpoint with request_uri and returns
// the login URL it redirects to.
func (w *acfWallet) authorize(requestURI string) string {
	w.f.t.Helper()
	status, location, body := browse(w.f.t, w.f.idOf(w.issuer, w.profile)+"/authorize?"+url.Values{"client_id": {w.clientID}, "request_uri": {requestURI}}.Encode())
	if status != http.StatusFound || !strings.HasPrefix(location, w.f.loginBase) {
		w.f.t.Fatalf("authorize: %d %q %s", status, location, body)
	}
	return location
}

// login returns from the login at the authorization server with a code for
// the state in loginURL.
func (w *acfWallet) login(loginURL string) (int, string, string) {
	w.f.t.Helper()
	u, _ := url.Parse(loginURL)
	state := u.Query().Get("state")
	return browse(w.f.t, w.f.idOf(w.issuer, w.profile)+"/callback?"+url.Values{"code": {"login-code-" + state}, "state": {state}}.Encode())
}

// code runs PAR, authorize and the login and returns the code the wallet
// receives.
func (w *acfWallet) code(headers map[string]string) string {
	w.f.t.Helper()
	status, location, body := w.login(w.authorize(w.requestURI(headers)))
	if status != http.StatusFound || !strings.HasPrefix(location, walletRedirect+"?") {
		w.f.t.Fatalf("callback: %d %q %s", status, location, body)
	}
	u, _ := url.Parse(location)
	return u.Query().Get("code")
}

// tokenForCode redeems code; extra overrides form parameters, dpopKey signs
// the proof (the wallet's own when nil).
func (w *acfWallet) tokenForCode(code string, extra url.Values, dpopKey *ecdsa.PrivateKey) (int, map[string]any) {
	w.f.t.Helper()
	if dpopKey == nil {
		dpopKey = w.dpopKey
	}
	id := w.f.idOf(w.issuer, w.profile)
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {w.verifier},
		"redirect_uri": {walletRedirect}, "client_id": {w.clientID},
	}
	for k, v := range extra {
		form[k] = v
	}
	return w.post("/token", strings.NewReader(form.Encode()), map[string]string{
		"OAuth-Client-Attestation":     w.wia,
		"OAuth-Client-Attestation-PoP": testwallet.PoP(w.f.t, w.wiaKey, w.clientID, id, w.challengeValue()),
		"DPoP":                         testwallet.DPoP(w.f.t, dpopKey, "POST", id+"/token", ""),
	}, "Content-Type", "application/x-www-form-urlencoded")
}

// --- P1-P3: metadata and the offer link -----------------------------------------

func TestAuthorizationCodeMetadata(t *testing.T) { // P1
	f := newFixture(t)
	_, body, _ := get(t, f.public.URL+"/.well-known/oauth-authorization-server/tax/eudi", "")
	var md map[string]any
	if err := json.Unmarshal([]byte(body), &md); err != nil {
		t.Fatal(err)
	}
	id := f.id("eudi")
	for field, want := range map[string]any{
		"pushed_authorization_request_endpoint":          id + "/par",
		"authorization_endpoint":                         id + "/authorize",
		"require_pushed_authorization_requests":          true,
		"authorization_response_iss_parameter_supported": true,
		"code_challenge_methods_supported":               []any{"S256"},
		"response_types_supported":                       []any{"code"},
		"grant_types_supported":                          []any{oid4vci.GrantPreAuthorizedCode, "authorization_code"},
	} {
		got, _ := json.Marshal(md[field])
		w, _ := json.Marshal(want)
		if string(got) != string(w) {
			t.Errorf("%s = %s, want %s", field, got, w)
		}
	}
	// K1: only the eudi profile offers the flow.
	for _, profile := range []string{"nl", "wallet"} {
		_, body, _ := get(t, f.public.URL+"/.well-known/oauth-authorization-server/tax/"+profile, "")
		if strings.Contains(body, "authorization_endpoint") || strings.Contains(body, `"authorization_code"`) {
			t.Errorf("profile %s offers the authorization code flow: %s", profile, body)
		}
	}
}

func TestTheLoginLinkOfAnOffer(t *testing.T) { // P3
	f := newFixture(t)
	_, body, _ := get(t, f.public.URL+"/disclosure-offers", "")
	var offers []struct {
		Profiles map[string]struct {
			LoginLink string `json:"login_link"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(body), &offers); err != nil || len(offers) != 1 {
		t.Fatalf("disclosure-offers = %s, %v", body, err)
	}
	link := offers[0].Profiles["eudi"].LoginLink
	raw, ok := strings.CutPrefix(link, "openid-credential-offer://?")
	if !ok {
		t.Fatalf("login_link = %q", link)
	}
	q, _ := url.ParseQuery(raw)
	var offer map[string]any
	if err := json.Unmarshal([]byte(q.Get("credential_offer")), &offer); err != nil {
		t.Fatal(err)
	}
	grant, _ := offer["grants"].(map[string]any)["authorization_code"].(map[string]any)
	if offer["credential_issuer"] != f.id("eudi") || grant["issuer_state"] != incomeOffer {
		t.Errorf("offer = %v, want the eudi issuer and issuer_state %s", offer, incomeOffer)
	}
	if ids, _ := offer["credential_configuration_ids"].([]any); len(ids) != 1 || ids[0] != incomeConfig {
		t.Errorf("credential_configuration_ids = %v", offer["credential_configuration_ids"])
	}
	if _, pre := offer["grants"].(map[string]any)[oid4vci.GrantPreAuthorizedCode]; pre {
		t.Error("the login link carries a pre-authorized code")
	}
	for _, profile := range []string{"nl", "wallet"} {
		if l := offers[0].Profiles[profile].LoginLink; l != "" {
			t.Errorf("profile %s has a login link %q", profile, l)
		}
	}
}

// --- P4-P9: the pushed authorization request --------------------------------------

func TestPushedAuthorizationRequest(t *testing.T) { // P4
	f := newFixture(t)
	w := f.acfWallet()
	status, body := w.par(w.parForm(), nil)
	if status != http.StatusCreated {
		t.Fatalf("PAR = %d %v, want 201", status, body)
	}
	if uri, _ := body["request_uri"].(string); !strings.HasPrefix(uri, parPrefix) || body["expires_in"] != float64(60) {
		t.Fatalf("PAR response = %v, want a request_uri and expires_in 60", body)
	}
}

func TestPushedAuthorizationRefusals(t *testing.T) { // P5-P8
	f := newFixture(t)
	w := f.acfWallet()
	other := f.wallet("nl")
	for name, tc := range map[string]struct {
		edit    func(url.Values)
		headers map[string]string
		status  int
		code    string
	}{
		"P5: no client attestation":              {nil, map[string]string{"OAuth-Client-Attestation": "", "OAuth-Client-Attestation-PoP": ""}, http.StatusUnauthorized, "invalid_client_attestation"},
		"P5: attestation of an untrusted wallet": {nil, map[string]string{"OAuth-Client-Attestation": other.wia}, http.StatusUnauthorized, "invalid_client_attestation"},
		"P5: client_id of another wallet":        {func(q url.Values) { q.Set("client_id", "someone-else") }, nil, http.StatusUnauthorized, "invalid_client_attestation"},
		"P6: no PKCE":                            {func(q url.Values) { q.Del("code_challenge"); q.Del("code_challenge_method") }, nil, http.StatusBadRequest, "invalid_request"},
		"P6: plain PKCE":                         {func(q url.Values) { q.Set("code_challenge_method", "plain") }, nil, http.StatusBadRequest, "invalid_request"},
		"P7: redirect_uri of another wallet":     {func(q url.Values) { q.Set("redirect_uri", "https://evil.test/cb") }, nil, http.StatusBadRequest, "invalid_request"},
		"P8: unknown issuer_state":               {func(q url.Values) { q.Set("issuer_state", "no-such-offer") }, nil, http.StatusBadRequest, "invalid_request"},
		"response_type token":                    {func(q url.Values) { q.Set("response_type", "token") }, nil, http.StatusBadRequest, "invalid_request"},
	} {
		t.Run(name, func(t *testing.T) {
			form := w.parForm()
			if tc.edit != nil {
				tc.edit(form)
			}
			status, body := w.par(form, tc.headers)
			if status != tc.status || body["error"] != tc.code || body["request_uri"] != nil {
				t.Fatalf("PAR = %d %v, want %d %s", status, body, tc.status, tc.code)
			}
		})
	}
	// K1: no PAR on the other profiles.
	nl := &acfWallet{wallet: f.wallet("nl")}
	nl.verifier, nl.challenge = w.verifier, w.challenge
	if status, body := nl.par(w.parForm(), nil); status != http.StatusNotFound {
		t.Fatalf("PAR on the nl profile = %d %v, want 404", status, body)
	}
}

// A DPoP proof at PAR binds the code to its key (RFC 9449 section 10).
func TestACodeBoundToADPoPKey(t *testing.T) { // P9
	f := newFixture(t)
	w := f.acfWallet()
	id := f.id("eudi")
	dpopHeader := map[string]string{"DPoP": testwallet.DPoP(t, w.dpopKey, "POST", id+"/par", "")}
	code := w.code(dpopHeader)
	if status, body := w.tokenForCode(code, nil, testpki.NewKey(t)); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("token with another DPoP key = %d %v, want invalid_grant", status, body)
	}
	code = w.code(map[string]string{"DPoP": testwallet.DPoP(t, w.dpopKey, "POST", id+"/par", "")})
	if status, body := w.tokenForCode(code, nil, nil); status != http.StatusOK {
		t.Fatalf("token with the bound key = %d %v, want 200", status, body)
	}
}

// --- P10-P14, C5, C8-C10: authorization request and login -----------------------------

func TestTheAuthorizationRequestNeedsAPushedRequest(t *testing.T) { // P10, P11
	f := newFixture(t)
	w := f.acfWallet()
	id := f.id("eudi")
	cases := map[string]url.Values{
		"P10: loose parameters": {"client_id": {w.clientID}, "response_type": {"code"}, "redirect_uri": {walletRedirect},
			"code_challenge": {w.challenge}, "code_challenge_method": {"S256"}, "issuer_state": {incomeOffer}},
		"P11: unknown request_uri": {"client_id": {w.clientID}, "request_uri": {parPrefix + "nope"}},
	}
	used := w.requestURI(nil)
	w.authorize(used)
	cases["P11: used request_uri"] = url.Values{"client_id": {w.clientID}, "request_uri": {used}}
	cases["P11: another client_id"] = url.Values{"client_id": {"someone-else"}, "request_uri": {w.requestURI(nil)}}
	expired := w.requestURI(nil)
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			status, location, _ := browse(t, id+"/authorize?"+q.Encode())
			if status != http.StatusBadRequest || location != "" {
				t.Fatalf("authorize = %d, Location %q; want 400 without redirect", status, location)
			}
		})
	}
	f.clock.Advance(61 * time.Second)
	if status, location, _ := browse(t, id+"/authorize?"+url.Values{"client_id": {w.clientID}, "request_uri": {expired}}.Encode()); status != http.StatusBadRequest || location != "" {
		t.Fatalf("authorize with an expired request_uri = %d %q, want 400", status, location)
	}
}

func TestAuthorizeSendsTheCitizenToTheLogin(t *testing.T) { // P12, C3
	f := newFixture(t)
	w := f.acfWallet()
	u, _ := url.Parse(w.authorize(w.requestURI(nil)))
	if q := u.Query(); q.Get("provider") != "tax" || q.Get("resource") != f.id("eudi") || q.Get("state") == "" {
		t.Fatalf("login URL = %s, want provider tax, the eudi issuer as resource and a state", u)
	}
}

func TestALoginReturnsACodeToTheWallet(t *testing.T) { // P13, C8
	f := newFixture(t)
	w := f.acfWallet()
	status, location, body := w.login(w.authorize(w.requestURI(nil)))
	if status != http.StatusFound {
		t.Fatalf("callback = %d %s", status, body)
	}
	u, _ := url.Parse(location)
	q := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != walletRedirect || q.Get("code") == "" || q.Get("state") != w.state || q.Get("iss") != f.id("eudi") {
		t.Fatalf("redirect = %s, want the wallet's redirect_uri with code, its state and iss", location)
	}
	// C8: the source was asked with the BSN from the login, marked as such,
	// and the offer's parameters.
	var sent map[string]any
	if err := json.Unmarshal(f.lastSourceBody(), &sent); err != nil {
		t.Fatal(err)
	}
	subject, _ := sent["subject"].(map[string]any)
	if subject["bsn"] != loginBSN || sent["origin"] != "login" {
		t.Errorf("source request = %v, want the BSN from the login and origin login", sent)
	}
	if f.lastSourceCall().Get("jaar") != "2025" {
		t.Errorf("source parameters = %v, want jaar 2025", f.lastSourceCall())
	}
}

func TestAnUnknownLoginStateGetsAnErrorPage(t *testing.T) { // C5
	f := newFixture(t)
	w := f.acfWallet()
	loginURL := w.authorize(w.requestURI(nil))
	if status, _, _ := w.login(loginURL); status != http.StatusFound {
		t.Fatal("first callback failed")
	}
	for name, q := range map[string]url.Values{
		"used state": func() url.Values {
			u, _ := url.Parse(loginURL)
			s := u.Query().Get("state")
			return url.Values{"code": {"login-code-" + s}, "state": {s}}
		}(),
		"unknown state": {"code": {"login-code-x"}, "state": {"x"}},
	} {
		t.Run(name, func(t *testing.T) {
			status, location, _ := browse(t, f.id("eudi")+"/callback?"+q.Encode())
			if status != http.StatusBadRequest || location != "" {
				t.Fatalf("callback = %d %q, want 400 without redirect", status, location)
			}
		})
	}
}

func TestALoginThatFailsGivesNoCode(t *testing.T) { // C6 (as the flow sees it)
	f := newFixture(t)
	f.login.err = errors.New("the access token is not for this issuer")
	w := f.acfWallet()
	status, location, _ := w.login(w.authorize(w.requestURI(nil)))
	if status != http.StatusBadRequest || location != "" {
		t.Fatalf("callback = %d %q, want 400 without a code", status, location)
	}
	if f.sourceCallCount() != 0 {
		t.Fatal("the source was asked without a valid login")
	}
}

func TestDenyingTheLoginTellsTheWallet(t *testing.T) { // C9
	f := newFixture(t)
	w := f.acfWallet()
	u, _ := url.Parse(w.authorize(w.requestURI(nil)))
	status, location, _ := browse(t, f.id("eudi")+"/callback?"+url.Values{"error": {"access_denied"}, "state": {u.Query().Get("state")}}.Encode())
	q := walletReturn(t, status, location)
	if q.Get("error") != "access_denied" || q.Get("state") != w.state || q.Get("iss") != f.id("eudi") || q.Get("code") != "" {
		t.Fatalf("redirect = %s, want access_denied with the wallet's state and iss", location)
	}
}

func walletReturn(t *testing.T, status int, location string) url.Values {
	t.Helper()
	if status != http.StatusFound || !strings.HasPrefix(location, walletRedirect+"?") {
		t.Fatalf("response = %d %q, want a redirect to the wallet", status, location)
	}
	u, _ := url.Parse(location)
	return u.Query()
}

// The pages with a way back to the wallet: no data (K6), source or login
// unavailable (K7).
func TestPagesWithAWayBackToTheWallet(t *testing.T) { // P14, C10
	for name, tc := range map[string]struct {
		setup   func(*fixture)
		heading string
		code    string
	}{
		"P14: the source has no data":   {func(f *fixture) { f.setSource(http.StatusNotFound, nil) }, "Geen gegevens gevonden", "access_denied"},
		"K7: the source is unavailable": {func(f *fixture) { f.setSource(http.StatusServiceUnavailable, nil) }, "Bron niet beschikbaar", "temporarily_unavailable"},
		"C10: the login is unavailable": {func(f *fixture) { f.login.err = issuance.ErrLoginUnavailable }, "Bron niet beschikbaar", "temporarily_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			tc.setup(f)
			w := f.acfWallet()
			status, location, body := w.login(w.authorize(w.requestURI(nil)))
			if status != http.StatusOK || location != "" || !strings.Contains(body, tc.heading) {
				t.Fatalf("callback = %d %q, want the page %q:\n%s", status, location, tc.heading, body)
			}
			back := backLink(t, body)
			u, _ := url.Parse(back)
			if q := u.Query(); u.Scheme+"://"+u.Host+u.Path != walletRedirect || q.Get("error") != tc.code || q.Get("state") != w.state || q.Get("iss") != f.id("eudi") {
				t.Fatalf("way back = %s, want %s with the wallet's state and iss", back, tc.code)
			}
			if strings.Contains(body, loginBSN) {
				t.Fatal("the page shows the BSN")
			}
		})
	}
	t.Run("C10: the login cannot be started", func(t *testing.T) {
		f := newFixture(t)
		f.login.urlErr = issuance.ErrLoginUnavailable
		w := f.acfWallet()
		status, location, body := browse(t, f.id("eudi")+"/authorize?"+url.Values{"client_id": {w.clientID}, "request_uri": {w.requestURI(nil)}}.Encode())
		if status != http.StatusOK || location != "" || !strings.Contains(body, "Bron niet beschikbaar") {
			t.Fatalf("authorize = %d %q:\n%s", status, location, body)
		}
		u, _ := url.Parse(backLink(t, body))
		if u.Query().Get("error") != "temporarily_unavailable" || u.Query().Get("state") != w.state {
			t.Fatalf("way back = %s", u)
		}
	})
}

// backLink returns the href of the button back to the wallet.
func backLink(t *testing.T, page string) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, `href="`+walletRedirect)
	if !ok {
		t.Fatalf("the page has no link back to the wallet:\n%s", page)
	}
	href, _, _ := strings.Cut(rest, `"`)
	unescaped := strings.ReplaceAll(href, "&amp;", "&")
	return walletRedirect + unescaped
}

// --- P15-P18: the token request with the code ----------------------------------------

func TestTheCodeBuysATokenAndACredential(t *testing.T) { // P15, P18
	f := newFixture(t)
	w := f.acfWallet()
	status, body := w.tokenForCode(w.code(nil), nil, nil)
	if status != http.StatusOK || body["token_type"] != "DPoP" {
		t.Fatalf("token = %d %v, want a DPoP token", status, body)
	}
	var tok oid4vci.TokenResponse
	remarshal(t, body, &tok)
	if len(tok.AuthorizationDetails) != 1 || tok.AuthorizationDetails[0].CredentialConfigurationID != incomeConfig || len(tok.AuthorizationDetails[0].CredentialIdentifiers) != 1 {
		t.Fatalf("authorization_details = %+v, want one income credential", tok.AuthorizationDetails)
	}
	ka := f.eudiWallet.KeyAttestation(t, []*ecdsa.PrivateKey{testpki.NewKey(t)}, w.nonce(), time.Now().Add(time.Hour))
	status, cred := w.credentialRequest(tok.AccessToken, w.dpopKey, oid4vci.CredentialRequest{
		CredentialIdentifier: tok.AuthorizationDetails[0].CredentialIdentifiers[0], Proofs: &oid4vci.Proofs{Attestation: []string{ka}},
	})
	if status != http.StatusOK || cred["credentials"] == nil {
		t.Fatalf("credential = %d %v", status, cred)
	}
}

func TestTheCodeIsBoundToThePushedRequest(t *testing.T) { // P16, P17
	f := newFixture(t)
	w := f.acfWallet()
	for name, extra := range map[string]url.Values{
		"P16: another code_verifier": {"code_verifier": {strings.Repeat("a", 43)}},
		"P16: another redirect_uri":  {"redirect_uri": {"eu.europa.ec.euidi://other"}},
		"P16: no code_verifier":      {"code_verifier": {""}},
	} {
		t.Run(name, func(t *testing.T) {
			if status, body := w.tokenForCode(w.code(nil), extra, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
				t.Fatalf("token = %d %v, want invalid_grant", status, body)
			}
		})
	}
	t.Run("P16: another wallet", func(t *testing.T) {
		code := w.code(nil)
		other := f.acfWallet()
		other.clientID = "another-wallet"
		other.wia = f.eudiWallet.WIA(t, other.clientID, other.wiaKey, time.Now().Add(time.Hour))
		other.verifier = w.verifier
		if status, body := other.tokenForCode(code, nil, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("token by another wallet = %d %v, want invalid_grant", status, body)
		}
	})
	t.Run("P17: used twice", func(t *testing.T) {
		code := w.code(nil)
		if status, _ := w.tokenForCode(code, nil, nil); status != http.StatusOK {
			t.Fatal("first redemption failed")
		}
		if status, body := w.tokenForCode(code, nil, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("second redemption = %d %v, want invalid_grant", status, body)
		}
	})
	t.Run("P17: after 60 seconds", func(t *testing.T) {
		// The code is made 59 seconds in the issuer's past and redeemed 2
		// seconds after it expired: the wallet's proofs use the real clock
		// and may lie at most 60 seconds from the issuer's.
		requestURI := w.requestURI(nil)
		f.clock.Advance(-59 * time.Second)
		_, location, _ := w.login(w.authorize(requestURI))
		u, _ := url.Parse(location)
		code := u.Query().Get("code")
		f.clock.Advance(61 * time.Second)
		if status, body := w.tokenForCode(code, nil, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("late redemption = %d %v, want invalid_grant", status, body)
		}
	})
	t.Run("a code is not a pre-authorized code", func(t *testing.T) {
		code := w.code(nil)
		if status, body := w.tokenRequest(code, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("code as pre-authorized_code = %d %v, want invalid_grant", status, body)
		}
	})
	t.Run("a pre-authorized code is not a code", func(t *testing.T) {
		if status, body := w.tokenForCode(f.preAuthCode("eudi"), nil, nil); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
			t.Fatalf("pre-authorized code as code = %d %v, want invalid_grant", status, body)
		}
	})
}

// C11: the BSN from the login never reaches the issuer's log.
func TestTheBSNIsNotLogged(t *testing.T) { // C11
	f := newFixture(t)
	w := f.acfWallet()
	w.tokenForCode(w.code(nil), nil, nil)
	f.setSource(http.StatusNotFound, nil)
	w.login(w.authorize(w.requestURI(nil)))
	if strings.Contains(f.logs.String(), loginBSN) {
		t.Fatalf("the log holds the BSN:\n%s", f.logs.String())
	}
}
