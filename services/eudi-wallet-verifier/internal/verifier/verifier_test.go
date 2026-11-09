package verifier_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/eudi-wallet-verifier/internal/verifier"
	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

const (
	pidType   = "urn:eudi:pid:nl:1"
	statusURI = "https://status.test/pid"
	pidIndex  = 3
	apiKey    = "secret"
	returnURL = "https://as.test/authorize/wallet/return?transaction=tx-1&session_token={session_token}"
)

var loginClaims = []string{"bsn", "family_name", "given_name", "birthdate", "nationalities"}

// fixture is the verifier with usecase asi-login, which returns
// same-device sessions, and qtsp-login, which does not; a trusted test-PID
// issuer, an untrusted one, a holder, and the status lists it can fetch.
type fixture struct {
	now       time.Time
	issuer    testpki.Cert
	signer    testpki.Cert
	untrusted testpki.Cert
	holder    *ecdsa.PrivateKey
	tokens    map[string]string
	aki       string
	public    *httptest.Server
	internal  *httptest.Server
}

var pidName = pkix.Name{CommonName: "Test PID issuer", Organization: []string{"GBO demo"}}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Now().Truncate(time.Second), holder: testpki.NewKey(t), tokens: map[string]string{}}
	root := testpki.NewRoot(t, "test pid root")
	f.issuer = root.Issue(t, testpki.Options{Subject: pidName, EKU: []asn1.ObjectIdentifier{oid4vp.CredentialSignerEKU}})
	f.signer = root.Issue(t, testpki.Options{Subject: pidName, EKU: []asn1.ObjectIdentifier{statuslist.SignerEKU}})
	other := testpki.NewRoot(t, "other pid root")
	f.untrusted = other.Issue(t, testpki.Options{Subject: pidName, EKU: []asn1.ObjectIdentifier{oid4vp.CredentialSignerEKU}})
	f.aki = base64.RawURLEncoding.EncodeToString(root.Cert.SubjectKeyId)
	f.tokens[statusURI] = f.statusToken(t, nil)

	readerRoot := testpki.NewRoot(t, "reader root")
	registrar := testpki.NewRoot(t, "registrar root")
	usecase := func(name string, claims []string, sameDevice bool) verifier.Usecase {
		wrpac := readerRoot.Issue(t, testpki.Options{Subject: testpki.OrganizationSubject(name, "GBO demo", "NTRNL-99990001")})
		signer, err := oid4vp.NewRequestSigner(wrpac.Key, wrpac.Chain)
		if err != nil {
			t.Fatal(err)
		}
		paths := make([][]string, len(claims))
		for i, c := range claims {
			paths[i] = []string{c}
		}
		return verifier.Usecase{Signer: signer, PIDType: pidType, Claims: claims, SameDeviceReturn: sameDevice,
			RegistrationCertificate: string(testwallet.VerifierRegistrationCertificate(t, registrar, name, pidType, paths...))}
	}
	cfg := verifier.Config{
		APIKey: apiKey, SessionTTL: 5 * time.Minute,
		PIDAnchors:            []x5c.Anchors{x5c.NewAnchors("test-pid", root.Cert)},
		PIDTrustedAuthorities: []string{f.aki},
		Usecases: map[string]verifier.Usecase{
			"asi-login":  usecase("asi-login", loginClaims, true),
			"qtsp-login": usecase("qtsp-login", loginClaims[1:], false),
		},
	}
	fetch := func(_ context.Context, uri string) (string, error) {
		if tok, ok := f.tokens[uri]; ok {
			return tok, nil
		}
		return "", errors.New("unreachable")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// The public URL is the public server's own, which exists only once
	// it listens; the handler reads it from the config at request time.
	var svc *verifier.Service
	f.public = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifier.PublicHandler(svc, log).ServeHTTP(w, r)
	}))
	t.Cleanup(f.public.Close)
	cfg.PublicURL = f.public.URL
	svc = verifier.New(cfg, fetch, func() time.Time { return f.now })
	f.internal = httptest.NewServer(verifier.InternalHandler(svc, log))
	t.Cleanup(f.internal.Close)
	return f
}

func (f *fixture) statusToken(t *testing.T, revoked []int) string {
	t.Helper()
	tok, err := statuslist.Sign(statuslist.Token{URI: statusURI, IssuedAt: f.now, Expires: f.now.Add(time.Hour), Size: 64, Revoked: revoked},
		f.signer.Key, x5c.Header(f.signer.Chain))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// pid issues a test PID by issuer with the claims given, or the login
// claims of Frouke Jansen.
func (f *fixture) pid(t *testing.T, issuer testpki.Cert, claims map[string]any) string {
	t.Helper()
	if claims == nil {
		claims = map[string]any{"bsn": "999991772", "given_name": "Frouke", "family_name": "Jansen", "birthdate": "2000-03-24",
			"nationalities": []string{"Nederlandse"}, "age_over_18": true}
	}
	s, err := sdjwtvc.NewSigner(issuer.Key, x5c.Header(issuer.Chain))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := s.Issue(sdjwtvc.Credential{
		VCT: pidType, IssuedAt: f.now, NotBefore: f.now.Add(-time.Hour), Expires: f.now.Add(24 * time.Hour),
		HolderKey: &jose.JSONWebKey{Key: &f.holder.PublicKey}, Status: &sdjwtvc.StatusReference{Index: pidIndex, URI: statusURI},
		Claims: claims,
	})
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

// do calls the API and decodes a JSON answer into out, if given.
func do(t *testing.T, method, target, key string, body io.Reader, contentType string, out any) int {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		if s, ok := out.(*string); ok {
			*s = string(raw)
		} else if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: %d %s", method, target, resp.StatusCode, raw)
		}
	}
	return resp.StatusCode
}

// start opens a session as the authorization server does.
func (f *fixture) start(t *testing.T, body map[string]any) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(body)
	var out map[string]any
	status := do(t, http.MethodPost, f.internal.URL+"/disclosure/sessions", apiKey, strings.NewReader(string(raw)), "application/json", &out)
	token, _ := out["session_token"].(string)
	return status, token
}

func (f *fixture) mustStart(t *testing.T, usecase, returnTemplate string) string {
	t.Helper()
	body := map[string]any{"usecase": usecase}
	if returnTemplate != "" {
		body["return_url_template"] = returnTemplate
	}
	status, token := f.start(t, body)
	if status != http.StatusOK || token == "" {
		t.Fatalf("start %s: %d", usecase, status)
	}
	return token
}

type statusResponse struct {
	Status string `json:"status"`
	UL     string `json:"ul"`
}

func (f *fixture) status(t *testing.T, token, sessionType string) statusResponse {
	t.Helper()
	var out statusResponse
	if code := do(t, http.MethodGet, f.public.URL+"/disclosure/sessions/"+token+"?session_type="+sessionType, "", nil, "", &out); code != http.StatusOK {
		t.Fatalf("status: %d", code)
	}
	return out
}

// request is a fetched and parsed request object.
type request struct {
	header map[string]any
	claims map[string]any
}

func (r request) str(name string) string { s, _ := r.claims[name].(string); return s }

// fetch follows the wallet link of sessionType to the request object.
func (f *fixture) fetch(t *testing.T, token, sessionType string) request {
	t.Helper()
	ul, err := url.Parse(f.status(t, token, sessionType).UL)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if code := do(t, http.MethodGet, ul.Query().Get("request_uri"), "", nil, "", &raw); code != http.StatusOK {
		t.Fatalf("request object: %d %s", code, raw)
	}
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := tok.UnsafeClaimsWithoutVerification(&claims); err != nil {
		t.Fatal(err)
	}
	var header map[string]any
	raw0, _ := base64.RawURLEncoding.DecodeString(strings.Split(raw, ".")[0])
	if err := json.Unmarshal(raw0, &header); err != nil {
		t.Fatal(err)
	}
	return request{header: header, claims: claims}
}

// present answers the request as a wallet does: the PID with the
// disclosures of keep, bound to the request, encrypted to its key.
func (f *fixture) present(t *testing.T, r request, cred string, keep ...string) url.Values {
	t.Helper()
	pres := testwallet.Present(t, testwallet.Select(t, cred, keep...), f.holder, r.str("client_id"), r.str("nonce"), f.now)
	return f.encrypt(t, r, map[string]any{"vp_token": map[string]any{"pid": []string{pres}}, "state": r.str("state")})
}

func (f *fixture) encrypt(t *testing.T, r request, body any) url.Values {
	t.Helper()
	keys, _ := json.Marshal(r.claims["client_metadata"].(map[string]any)["jwks"])
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(keys, &set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("response key: %v", err)
	}
	raw, _ := json.Marshal(body)
	return url.Values{"response": {testwallet.EncryptRequest(t, raw, set.Keys[0], jose.A128GCM, nil)}}
}

// post sends the wallet's form to the response_uri.
func (f *fixture) post(t *testing.T, r request, form url.Values) (int, map[string]any) {
	t.Helper()
	var out map[string]any
	code := do(t, http.MethodPost, r.str("response_uri"), "", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", &out)
	return code, out
}

type disclosedResponse []struct {
	ID           string `json:"id"`
	Attestations []struct {
		Type       string         `json:"attestation_type"`
		Format     string         `json:"format"`
		Attributes map[string]any `json:"attributes"`
	} `json:"attestations"`
}

func (f *fixture) disclosed(t *testing.T, token, nonce string) (int, string) {
	t.Helper()
	target := f.internal.URL + "/disclosure/sessions/" + token + "/disclosed_attributes"
	if nonce != "" {
		target += "?" + url.Values{"nonce": {nonce}}.Encode()
	}
	var raw string
	code := do(t, http.MethodGet, target, apiKey, nil, "", &raw)
	return code, raw
}

func errorType(t *testing.T, raw string) string {
	t.Helper()
	var body struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(raw), &body)
	return body.Type
}

// T1: the internal API refuses a missing or wrong API key.
func TestStartAPIKey(t *testing.T) {
	f := newFixture(t)
	for _, key := range []string{"", "wrong"} {
		var out map[string]any
		code := do(t, http.MethodPost, f.internal.URL+"/disclosure/sessions", key, strings.NewReader(`{"usecase":"qtsp-login"}`), "application/json", &out)
		if code != http.StatusUnauthorized || out["session_token"] != nil {
			t.Errorf("key %q: %d %v, want 401 and no session", key, code, out)
		}
	}
	// With the key, an unknown session is not found; without it, refused.
	if code, _ := f.disclosed(t, "unknown", ""); code != http.StatusNotFound {
		t.Errorf("disclosed attributes of an unknown session: %d, want 404", code)
	}
	var raw string
	if code := do(t, http.MethodGet, f.internal.URL+"/disclosure/sessions/x/disclosed_attributes", "", nil, "", &raw); code != http.StatusUnauthorized {
		t.Errorf("disclosed attributes without key: %d, want 401", code)
	}
}

// T2: a query outside the usecase's registration, an unknown usecase, and
// a return URL template that does not match the usecase are refused.
func TestStartRefuses(t *testing.T) {
	f := newFixture(t)
	q := func(vct string, claims ...string) map[string]any {
		paths := make([]map[string]any, len(claims))
		for i, c := range claims {
			paths[i] = map[string]any{"path": []string{c}}
		}
		return map[string]any{"credentials": []map[string]any{{"id": "pid", "format": "dc+sd-jwt", "meta": map[string]any{"vct_values": []string{vct}}, "claims": paths}}}
	}
	cases := map[string]map[string]any{
		"unknown usecase":            {"usecase": "other"},
		"claim not registered":       {"usecase": "qtsp-login", "dcql_query": q(pidType, "bsn")},
		"other type":                 {"usecase": "qtsp-login", "dcql_query": q("urn:eudi:pid:1", "given_name")},
		"nested path":                {"usecase": "qtsp-login", "dcql_query": map[string]any{"credentials": []map[string]any{{"id": "pid", "format": "dc+sd-jwt", "meta": map[string]any{"vct_values": []string{pidType}}, "claims": []map[string]any{{"path": []string{"given_name", "x"}}}}}}},
		"two credential queries":     {"usecase": "qtsp-login", "dcql_query": map[string]any{"credentials": []any{q(pidType, "given_name")["credentials"].([]map[string]any)[0], q(pidType, "given_name")["credentials"].([]map[string]any)[0]}}},
		"same-device without return": {"usecase": "asi-login"},
		"return where none is taken": {"usecase": "qtsp-login", "return_url_template": returnURL},
		"return not http(s)":         {"usecase": "asi-login", "return_url_template": "javascript:alert(1)//{session_token}"},
	}
	for name, body := range cases {
		if status, token := f.start(t, body); status != http.StatusBadRequest || token != "" {
			t.Errorf("%s: %d %q, want 400 and no session", name, status, token)
		}
	}
	// Within the registration it is accepted.
	if status, _ := f.start(t, map[string]any{"usecase": "asi-login", "return_url_template": returnURL, "dcql_query": q(pidType, loginClaims...)}); status != http.StatusOK {
		t.Errorf("registered query: %d, want 200", status)
	}
}

// T3: a new session is CREATED with a link per session type.
func TestStatusCreated(t *testing.T) {
	f := newFixture(t)
	token := f.mustStart(t, "asi-login", returnURL)
	links := map[string]string{}
	for _, st := range []string{verifier.CrossDevice, verifier.SameDevice} {
		out := f.status(t, token, st)
		if out.Status != "CREATED" || !strings.HasPrefix(out.UL, "openid4vp://?") {
			t.Fatalf("%s: %+v", st, out)
		}
		ul, _ := url.Parse(out.UL)
		if !strings.HasPrefix(ul.Query().Get("client_id"), oid4vp.ClientIDPrefixHash) {
			t.Errorf("%s: client_id %q, want x509_hash", st, ul.Query().Get("client_id"))
		}
		ru, _ := url.Parse(ul.Query().Get("request_uri"))
		if ru.Query().Get("session_type") != st {
			t.Errorf("%s: request_uri %q has another session type", st, ru)
		}
		links[st] = out.UL
	}
	if links[verifier.CrossDevice] == links[verifier.SameDevice] {
		t.Error("the links of the session types are the same")
	}
	var raw string
	if code := do(t, http.MethodGet, f.public.URL+"/disclosure/sessions/"+token+"?session_type=other", "", nil, "", &raw); code != http.StatusBadRequest {
		t.Errorf("unknown session type: %d, want 400", code)
	}
	if code := do(t, http.MethodGet, f.public.URL+"/disclosure/sessions/unknown?session_type=cross_device", "", nil, "", &raw); code != http.StatusNotFound {
		t.Errorf("unknown session: %d, want 404", code)
	}
}

// T4: the wallet fetches the request object once; the session then waits.
func TestRequestObject(t *testing.T) {
	f := newFixture(t)
	token := f.mustStart(t, "asi-login", returnURL)
	ul, _ := url.Parse(f.status(t, token, verifier.CrossDevice).UL)
	r := f.fetch(t, token, verifier.CrossDevice)

	if r.header["typ"] != oid4vp.RequestObjectType {
		t.Errorf("typ %v", r.header["typ"])
	}
	chain, _ := r.header["x5c"].([]any)
	if len(chain) == 0 {
		t.Fatal("no x5c")
	}
	der, _ := base64.StdEncoding.DecodeString(chain[0].(string))
	sum := sha256.Sum256(der)
	if want := oid4vp.ClientIDPrefixHash + base64.RawURLEncoding.EncodeToString(sum[:]); r.str("client_id") != want || ul.Query().Get("client_id") != want {
		t.Errorf("client_id %q (link %q), want the x509_hash of the WRPAC %q", r.str("client_id"), ul.Query().Get("client_id"), want)
	}
	if r.str("response_mode") != oid4vp.ResponseModeJWT || r.str("response_uri") != f.public.URL+"/disclosure/sessions/"+token+"/response" {
		t.Errorf("response_mode %q, response_uri %q", r.str("response_mode"), r.str("response_uri"))
	}
	cq := r.claims["dcql_query"].(map[string]any)["credentials"].([]any)[0].(map[string]any)
	ta, _ := json.Marshal(cq["trusted_authorities"])
	if want := `[{"type":"aki","values":["` + f.aki + `"]}]`; string(ta) != want {
		t.Errorf("trusted_authorities %s, want %s", ta, want)
	}
	claims, _ := json.Marshal(cq["claims"])
	if want := `[{"path":["bsn"]},{"path":["family_name"]},{"path":["given_name"]},{"path":["birthdate"]},{"path":["nationalities"]}]`; string(claims) != want {
		t.Errorf("claims %s, want %s", claims, want)
	}
	info := r.claims["verifier_info"].([]any)[0].(map[string]any)
	if data, _ := info["data"].(string); info["format"] != oid4vp.FormatRegistrationCert || strings.Count(data, ".") != 2 {
		t.Errorf("verifier_info %v, want the WRPRC as a compact JWT", info)
	}
	if got := f.status(t, token, verifier.CrossDevice); got.Status != "WAITING_FOR_RESPONSE" || got.UL != "" {
		t.Errorf("after the fetch: %+v, want WAITING_FOR_RESPONSE without link", got)
	}
	var raw string
	if code := do(t, http.MethodGet, ul.Query().Get("request_uri"), "", nil, "", &raw); code != http.StatusBadRequest {
		t.Errorf("second fetch: %d, want 400", code)
	}
}

// T5: a valid cross-device response makes the session DONE; the
// attributes come without a nonce, the requested claims only.
func TestCrossDevice(t *testing.T) {
	f := newFixture(t)
	token := f.mustStart(t, "asi-login", returnURL)
	r := f.fetch(t, token, verifier.CrossDevice)
	code, out := f.post(t, r, f.present(t, r, f.pid(t, f.issuer, nil), loginClaims...))
	if code != http.StatusOK || out["redirect_uri"] != nil {
		t.Fatalf("response: %d %v, want 200 without redirect_uri", code, out)
	}
	if got := f.status(t, token, verifier.CrossDevice).Status; got != "DONE" {
		t.Fatalf("status %s, want DONE", got)
	}
	code, raw := f.disclosed(t, token, "")
	if code != http.StatusOK {
		t.Fatalf("disclosed: %d %s", code, raw)
	}
	var d disclosedResponse
	_ = json.Unmarshal([]byte(raw), &d)
	if len(d) != 1 || d[0].ID != "pid" || len(d[0].Attestations) != 1 || d[0].Attestations[0].Type != pidType || d[0].Attestations[0].Format != "dc+sd-jwt" {
		t.Fatalf("disclosed %s", raw)
	}
	attrs, _ := json.Marshal(d[0].Attestations[0].Attributes)
	if want := `{"birthdate":"2000-03-24","bsn":"999991772","family_name":"Jansen","given_name":"Frouke","nationalities":["Nederlandse"]}`; string(attrs) != want {
		t.Errorf("attributes %s, want %s", attrs, want)
	}
}

// T6: a valid same-device response returns the wallet to the relying
// party with a nonce; only that nonce releases the attributes.
func TestSameDevice(t *testing.T) {
	f := newFixture(t)
	token := f.mustStart(t, "asi-login", returnURL)
	r := f.fetch(t, token, verifier.SameDevice)
	code, out := f.post(t, r, f.present(t, r, f.pid(t, f.issuer, nil), loginClaims...))
	redirect, _ := out["redirect_uri"].(string)
	if code != http.StatusOK || redirect == "" {
		t.Fatalf("response: %d %v, want 200 with redirect_uri", code, out)
	}
	u, err := url.Parse(redirect)
	if err != nil || u.Host != "as.test" || u.Path != "/authorize/wallet/return" ||
		u.Query().Get("transaction") != "tx-1" || u.Query().Get("session_token") != token || u.Query().Get("nonce") == "" {
		t.Fatalf("redirect_uri %q", redirect)
	}
	for name, nonce := range map[string]string{"no nonce": "", "wrong nonce": "wrong"} {
		if code, raw := f.disclosed(t, token, nonce); code != http.StatusUnauthorized || errorType(t, raw) != "nonce" || strings.Contains(raw, "999991772") {
			t.Errorf("%s: %d %s, want 401 nonce without the PID", name, code, raw)
		}
	}
	if code, raw := f.disclosed(t, token, u.Query().Get("nonce")); code != http.StatusOK || !strings.Contains(raw, "999991772") {
		t.Errorf("with the nonce: %d %s, want the PID", code, raw)
	}
}

// A same-device session of a usecase without a return URL ends at the
// verifier, like a cross-device one.
func TestSameDeviceWithoutReturn(t *testing.T) {
	f := newFixture(t)
	token := f.mustStart(t, "qtsp-login", "")
	r := f.fetch(t, token, verifier.SameDevice)
	code, out := f.post(t, r, f.present(t, r, f.pid(t, f.issuer, nil), loginClaims[1:]...))
	if code != http.StatusOK || out["redirect_uri"] != nil {
		t.Fatalf("response: %d %v, want 200 without redirect_uri", code, out)
	}
	if code, raw := f.disclosed(t, token, ""); code != http.StatusOK || strings.Contains(raw, "bsn") {
		t.Errorf("disclosed: %d %s, want the attributes without a BSN", code, raw)
	}
}

// T7: a wallet error ends the session without attributes: access_denied
// as CANCELLED, another error as FAILED. An error without the session's
// state is refused and leaves the session waiting.
func TestWalletError(t *testing.T) {
	for wErr, want := range map[string]string{"access_denied": "CANCELLED", "invalid_request": "FAILED"} {
		f := newFixture(t)
		token := f.mustStart(t, "asi-login", returnURL)
		r := f.fetch(t, token, verifier.SameDevice)
		if code, _ := f.post(t, r, url.Values{"error": {wErr}, "state": {"other"}}); code != http.StatusBadRequest {
			t.Errorf("%s with another state: %d, want 400", wErr, code)
		}
		if got := f.status(t, token, verifier.CrossDevice).Status; got != "WAITING_FOR_RESPONSE" {
			t.Errorf("%s with another state: status %s, want WAITING_FOR_RESPONSE", wErr, got)
		}
		code, out := f.post(t, r, url.Values{"error": {wErr}, "state": {r.str("state")}})
		if code != http.StatusOK || out["redirect_uri"] != nil {
			t.Errorf("%s: %d %v, want 200 {}", wErr, code, out)
		}
		if got := f.status(t, token, verifier.CrossDevice).Status; got != want {
			t.Errorf("%s: status %s, want %s", wErr, got, want)
		}
		if code, raw := f.disclosed(t, token, ""); code != http.StatusBadRequest || errorType(t, raw) != "session_state" {
			t.Errorf("%s: disclosed %d %s, want 400 session_state", wErr, code, raw)
		}
	}
}

// T8: a response the kit refuses fails the session; the reason is not
// given to the relying party, and there are no attributes.
func TestRefusedResponse(t *testing.T) {
	cases := []struct {
		name   string
		answer func(t *testing.T, f *fixture, r request) url.Values
		setup  func(t *testing.T, f *fixture)
		code   int
	}{
		{name: "untrusted issuer", code: http.StatusBadRequest, answer: func(t *testing.T, f *fixture, r request) url.Values {
			return f.present(t, r, f.pid(t, f.untrusted, nil), loginClaims...)
		}},
		{name: "key binding for another nonce", code: http.StatusBadRequest, answer: func(t *testing.T, f *fixture, r request) url.Values {
			pres := testwallet.Present(t, testwallet.Select(t, f.pid(t, f.issuer, nil), loginClaims...), f.holder, r.str("client_id"), "other", f.now)
			return f.encrypt(t, r, map[string]any{"vp_token": map[string]any{"pid": []string{pres}}, "state": r.str("state")})
		}},
		{name: "claim not asked for", code: http.StatusBadRequest, answer: func(t *testing.T, f *fixture, r request) url.Values {
			return f.present(t, r, f.pid(t, f.issuer, nil), append(loginClaims, "age_over_18")...)
		}},
		{name: "PID without bsn", code: http.StatusBadRequest, answer: func(t *testing.T, f *fixture, r request) url.Values {
			return f.present(t, r, f.pid(t, f.issuer, map[string]any{"given_name": "Frouke", "family_name": "Jansen", "birthdate": "2000-03-24", "nationalities": []string{"Nederlandse"}}), loginClaims...)
		}},
		{name: "revoked PID", code: http.StatusBadRequest, setup: func(t *testing.T, f *fixture) { f.tokens[statusURI] = f.statusToken(t, []int{pidIndex}) },
			answer: func(t *testing.T, f *fixture, r request) url.Values {
				return f.present(t, r, f.pid(t, f.issuer, nil), loginClaims...)
			}},
		{name: "status undetermined", code: http.StatusInternalServerError, setup: func(t *testing.T, f *fixture) { delete(f.tokens, statusURI) },
			answer: func(t *testing.T, f *fixture, r request) url.Values {
				return f.present(t, r, f.pid(t, f.issuer, nil), loginClaims...)
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			if c.setup != nil {
				c.setup(t, f)
			}
			token := f.mustStart(t, "asi-login", returnURL)
			r := f.fetch(t, token, verifier.SameDevice)
			code, out := f.post(t, r, c.answer(t, f, r))
			if code != c.code || out["redirect_uri"] != nil {
				t.Errorf("response: %d %v, want %d without redirect_uri", code, out, c.code)
			}
			if b, _ := json.Marshal(out); strings.Contains(string(b), "description") {
				t.Errorf("the wallet got a reason: %s", b)
			}
			if got := f.status(t, token, verifier.CrossDevice).Status; got != "FAILED" {
				t.Errorf("status %s, want FAILED", got)
			}
			if code, raw := f.disclosed(t, token, ""); code != http.StatusBadRequest || strings.Contains(raw, "Frouke") {
				t.Errorf("disclosed: %d %s, want 400 without attributes", code, raw)
			}
		})
	}
}

// T9: a session takes one response, and expires after its lifetime.
func TestOnceAndExpiry(t *testing.T) {
	f := newFixture(t)
	token := f.mustStart(t, "asi-login", returnURL)
	r := f.fetch(t, token, verifier.CrossDevice)
	answer := f.present(t, r, f.pid(t, f.issuer, nil), loginClaims...)
	if code, _ := f.post(t, r, answer); code != http.StatusOK {
		t.Fatalf("first response: %d", code)
	}
	if code, _ := f.post(t, r, answer); code != http.StatusBadRequest {
		t.Errorf("second response: %d, want 400", code)
	}

	waiting := f.mustStart(t, "asi-login", returnURL)
	rw := f.fetch(t, waiting, verifier.CrossDevice)
	created := f.mustStart(t, "asi-login", returnURL)
	createdUL, _ := url.Parse(f.status(t, created, verifier.CrossDevice).UL)
	f.now = f.now.Add(5*time.Minute + time.Second)
	for name, tok := range map[string]string{"created": created, "waiting": waiting} {
		if got := f.status(t, tok, verifier.CrossDevice); got.Status != "EXPIRED" || got.UL != "" {
			t.Errorf("%s: %+v, want EXPIRED", name, got)
		}
	}
	var raw string
	if code := do(t, http.MethodGet, createdUL.Query().Get("request_uri"), "", nil, "", &raw); code != http.StatusBadRequest {
		t.Errorf("request after expiry: %d, want 400", code)
	}
	if code, _ := f.post(t, rw, f.present(t, rw, f.pid(t, f.issuer, nil), loginClaims...)); code != http.StatusBadRequest {
		t.Errorf("response after expiry: %d, want 400", code)
	}
	// A finished session stays readable for the relying party.
	if code, _ := f.disclosed(t, token, ""); code != http.StatusOK {
		t.Errorf("disclosed after the lifetime: %d, want 200", code)
	}
}
