package httpapi_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/httpapi"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/eudi-credential-issuer/internal/source"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

var (
	mdlIssuerEKU  = asn1.ObjectIdentifier{1, 0, 18013, 5, 1, 2}
	statusListEKU = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 127}
)

const (
	incomeConfig = "income"
	incomeVCT    = "https://source.example.test/types/income/v1"
	nlClient     = "https://wallet.edi.rijksoverheid.nl"
	eudiClient   = "eudiw-abca"
)

// fixture is a running issuer with two profiles, a fake source and a
// controllable clock.
type fixture struct {
	t          *testing.T
	public     *httptest.Server
	internal   *httptest.Server
	issuerRoot testpki.Cert
	accessRoot testpki.Cert
	nlWallet   testwallet.Provider
	eudiWallet testwallet.Provider
	clock      *clock
	// sourceStatus and sourceDocs drive the fake source.
	sourceMu     sync.Mutex
	sourceStatus int
	sourceDocs   []map[string]any
	sourceCalls  []url.Values
}

// clock runs with real time plus an offset, so proofs the test wallet
// makes with time.Now stay consistent with the issuer's clock.
type clock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, clock: &clock{}, sourceStatus: http.StatusOK}
	f.sourceDocs = []map[string]any{{
		"id": "doc-1", "format": "dc+sd-jwt", "attestation_type": incomeVCT,
		"attributes": map[string]any{"year": "2025", "income": 41000, "address": map[string]any{"city": "Utrecht"}},
	}}
	src := httptest.NewServer(http.HandlerFunc(f.serveSource))
	t.Cleanup(src.Close)

	subject := pkix.Name{CommonName: "Issuer", Organization: []string{"GBO Demo"}}
	f.issuerRoot = testpki.NewRoot(t, "issuer root")
	issuerCA := f.issuerRoot.IssueCA(t, "issuer CA")
	issuerCert := issuerCA.Issue(t, testpki.Options{Subject: subject, EKU: []asn1.ObjectIdentifier{mdlIssuerEKU}})
	statusCert := issuerCA.Issue(t, testpki.Options{Subject: subject, EKU: []asn1.ObjectIdentifier{statusListEKU}})
	f.accessRoot = testpki.NewRoot(t, "access root")
	access := f.accessRoot.Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "access", Organization: []string{"GBO Demo"}}})
	f.nlWallet = testwallet.NewProvider(t, "nl wallet provider")
	f.eudiWallet = testwallet.NewProvider(t, "eudi wallet provider")

	var handlers struct{ public, internal http.Handler }
	f.public = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlers.public.ServeHTTP(w, r) }))
	t.Cleanup(f.public.Close)
	f.internal = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlers.internal.ServeHTTP(w, r) }))
	t.Cleanup(f.internal.Close)

	cfg := issuance.Config{
		PublicURL: f.public.URL,
		Profiles: []issuance.Profile{
			{Name: "nl", Kind: issuance.KindNL, WalletProviders: []x5c.Anchors{f.nlWallet.Anchors("nl")},
				AccessKey: access.Key, AccessChain: access.Chain, RegistrationCertificate: []byte("wrprc"),
				UniversalLinkBase: "https://app.example.test/deeplink"},
			{Name: "eudi", Kind: issuance.KindEUDI, WalletProviders: []x5c.Anchors{f.eudiWallet.Anchors("eudi")},
				KeyAttestationProviders: []x5c.Anchors{f.eudiWallet.Anchors("eudi")},
				KeyStorage:              []string{"iso_18045_high"}, UserAuthentication: []string{"iso_18045_high"},
				AccessKey: access.Key, AccessChain: access.Chain},
		},
		Configurations: []issuance.CredentialConfiguration{{
			ID: incomeConfig, VCT: incomeVCT, Scope: "income", ValidDays: 365,
			SourceURL: src.URL + "/attestations/tax/income", StatusList: "income",
			Display: []issuance.Display{{Name: "Income statement", Locale: "en"}},
			Claims: []issuance.Claim{
				{Path: []string{"year"}, Mandatory: true}, {Path: []string{"income"}}, {Path: []string{"address", "city"}},
			},
		}},
		IssuerKey: issuerCert.Key, IssuerChain: issuerCert.Chain,
		StatusKey: statusCert.Key, StatusChain: statusCert.Chain,
		StatusListSize: 1024,
	}
	store := memstore.New()
	svc, err := issuance.New(cfg, &source.Client{HTTP: src.Client()}, store, store, store, f.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handlers.public = httpapi.Public(svc, log)
	handlers.internal = httpapi.Internal(svc, log)
	return f
}

func (f *fixture) serveSource(w http.ResponseWriter, r *http.Request) {
	f.sourceMu.Lock()
	defer f.sourceMu.Unlock()
	f.sourceCalls = append(f.sourceCalls, r.URL.Query())
	if f.sourceStatus != http.StatusOK {
		w.WriteHeader(f.sourceStatus)
		return
	}
	_ = json.NewEncoder(w).Encode(f.sourceDocs)
}

// setSource changes what the fake source answers.
func (f *fixture) setSource(status int, docs []map[string]any) {
	f.sourceMu.Lock()
	defer f.sourceMu.Unlock()
	f.sourceStatus = status
	if docs != nil {
		f.sourceDocs = docs
	}
}

func (f *fixture) lastSourceCall() url.Values {
	f.sourceMu.Lock()
	defer f.sourceMu.Unlock()
	return f.sourceCalls[len(f.sourceCalls)-1]
}

func (f *fixture) id(profile string) string { return f.public.URL + "/" + profile }

// offer creates an offer on the internal API and returns the status and
// the decoded body.
func (f *fixture) offer(body map[string]any) (int, map[string]any) {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(f.internal.URL+"/offers", "application/json", bytes.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	return decode(f.t, resp)
}

// preAuthCode creates an offer for the income configuration and returns
// its pre-authorized code.
func (f *fixture) preAuthCode(profile string) string {
	f.t.Helper()
	return f.preAuthCodeRef(profile, "")
}

// preAuthCodeRef is preAuthCode with a revocation reference.
func (f *fixture) preAuthCodeRef(profile, reference string) string {
	f.t.Helper()
	status, body := f.offer(map[string]any{
		"profile": profile, "credential_configuration_ids": []string{incomeConfig},
		"subject": map[string]any{"bsn": "999990019"}, "parameters": map[string]string{"jaar": "2025"},
		"reference": reference,
	})
	if status != http.StatusCreated {
		f.t.Fatalf("offer: status %d %v", status, body)
	}
	grants := body["credential_offer"].(map[string]any)["grants"].(map[string]any)
	return grants[oid4vci.GrantPreAuthorizedCode].(map[string]any)["pre-authorized_code"].(string)
}

// wallet is one wallet instance: its WIA key and DPoP key.
type wallet struct {
	f        *fixture
	profile  string
	clientID string
	provider testwallet.Provider
	wiaKey   *ecdsa.PrivateKey
	dpopKey  *ecdsa.PrivateKey
	wia      string
}

func (f *fixture) wallet(profile string) *wallet {
	w := &wallet{f: f, profile: profile, wiaKey: testpki.NewKey(f.t), dpopKey: testpki.NewKey(f.t)}
	w.clientID, w.provider = nlClient, f.nlWallet
	if profile == "eudi" {
		w.clientID, w.provider = eudiClient, f.eudiWallet
	}
	w.wia = w.provider.WIA(f.t, w.clientID, w.wiaKey, time.Now().Add(time.Hour))
	return w
}

func (w *wallet) challenge() string {
	w.f.t.Helper()
	status, body := w.post("/challenge", nil, nil)
	if status != http.StatusOK {
		w.f.t.Fatalf("challenge: %d %v", status, body)
	}
	return body["attestation_challenge"].(string)
}

func (w *wallet) nonce() string {
	w.f.t.Helper()
	status, body := w.post("/nonce", nil, nil)
	if status != http.StatusOK {
		w.f.t.Fatalf("nonce: %d %v", status, body)
	}
	return body["c_nonce"].(string)
}

// tokenRequest posts a token request; headers override the defaults.
func (w *wallet) tokenRequest(code string, headers map[string]string) (int, map[string]any) {
	w.f.t.Helper()
	return w.tokenRequestWith(code, headers, nil)
}

// tokenRequestForm posts a token request with extra form parameters.
func (w *wallet) tokenRequestForm(code string, extra url.Values) (int, map[string]any) {
	w.f.t.Helper()
	return w.tokenRequestWith(code, nil, extra)
}

func (w *wallet) tokenRequestWith(code string, headers map[string]string, extra url.Values) (int, map[string]any) {
	w.f.t.Helper()
	h := map[string]string{
		"OAuth-Client-Attestation":     w.wia,
		"OAuth-Client-Attestation-PoP": testwallet.PoP(w.f.t, w.wiaKey, w.clientID, w.f.id(w.profile), w.challenge()),
		"DPoP":                         testwallet.DPoP(w.f.t, w.dpopKey, "POST", w.f.id(w.profile)+"/token", ""),
	}
	for k, v := range headers {
		if v == "" {
			delete(h, k)
		} else {
			h[k] = v
		}
	}
	form := url.Values{"grant_type": {oid4vci.GrantPreAuthorizedCode}, "pre-authorized_code": {code}}
	for k, v := range extra {
		form[k] = v
	}
	return w.post("/token", strings.NewReader(form.Encode()), h, "Content-Type", "application/x-www-form-urlencoded")
}

func (w *wallet) token(code string) oid4vci.TokenResponse {
	w.f.t.Helper()
	status, body := w.tokenRequest(code, nil)
	if status != http.StatusOK {
		w.f.t.Fatalf("token: %d %v", status, body)
	}
	var resp oid4vci.TokenResponse
	remarshal(w.f.t, body, &resp)
	return resp
}

// credentialRequest posts a credential request with a DPoP proof made with
// dpopKey.
func (w *wallet) credentialRequest(accessToken string, dpopKey *ecdsa.PrivateKey, req oid4vci.CredentialRequest) (int, map[string]any) {
	w.f.t.Helper()
	raw, _ := json.Marshal(req)
	return w.post("/credential", bytes.NewReader(raw), map[string]string{
		"Authorization": "DPoP " + accessToken,
		"DPoP":          testwallet.DPoP(w.f.t, dpopKey, "POST", w.f.id(w.profile)+"/credential", accessToken),
	}, "Content-Type", "application/json")
}

func (w *wallet) post(path string, body io.Reader, headers map[string]string, extra ...string) (int, map[string]any) {
	w.f.t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.f.id(w.profile)+path, body)
	if err != nil {
		w.f.t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		req.Header.Set(extra[i], extra[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.f.t.Fatal(err)
	}
	return decode(w.f.t, resp)
}

func decode(t *testing.T, resp *http.Response) (int, map[string]any) {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body
}

func remarshal(t *testing.T, in, out any) {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, u string, accept string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}
