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
	deathConfig  = "death"
	deathVCT     = "https://source.example.test/types/death/v1"
	nlClient     = "https://wallet.edi.rijksoverheid.nl"
	eudiClient   = "eudiw-abca"
)

// fixture is a running issuer with two profiles, a fake source and a
// controllable clock.
type fixture struct {
	t        *testing.T
	public   *httptest.Server
	internal *httptest.Server
	// issuerRoot and accessRoot are the roots of the tax provider;
	// civilIssuerRoot and civilAccessRoot those of the civil provider.
	issuerRoot      testpki.Cert
	accessRoot      testpki.Cert
	civilIssuerRoot testpki.Cert
	civilAccessRoot testpki.Cert
	// civilRegistration is the civil provider's registration certificate.
	civilRegistration []byte
	nlWallet          testwallet.Provider
	eudiWallet        testwallet.Provider
	clock             *clock
	// sourceStatus and sourceDocs drive the fake source.
	sourceMu     sync.Mutex
	sourceStatus int
	sourceDocs   []map[string]any
	sourceCalls  []url.Values
	sourceBodies [][]byte
	// pidRoot, pidIssuer and pidStatus are a test PID provider the issuer
	// trusts; pidRevoked and pidStatusDown drive its status list.
	pidRoot       testpki.Cert
	pidIssuer     testpki.Cert
	pidStatus     testpki.Cert
	pidRevoked    []int
	pidStatusDown bool
	// logs holds what the issuer logged.
	logs *syncBuffer
	// login stands in for GBO's authorization server.
	login *fakeLogin
}

// syncBuffer is a bytes.Buffer safe for the concurrent writes of handlers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
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
	f := &fixture{t: t, clock: &clock{}, sourceStatus: http.StatusOK, logs: &syncBuffer{}, login: newFakeLogin()}
	f.sourceDocs = []map[string]any{{
		"id": "doc-1", "format": "dc+sd-jwt", "attestation_type": incomeVCT,
		"attributes": map[string]any{"year": "2025", "income": 41000, "address": map[string]any{"city": "Utrecht"}},
	}}
	src := httptest.NewServer(http.HandlerFunc(f.serveSource))
	t.Cleanup(src.Close)

	var taxProvider, civilProvider issuance.Provider
	taxProvider, f.issuerRoot, f.accessRoot = newProvider(t, "tax", incomeVCT)
	civilProvider, f.civilIssuerRoot, f.civilAccessRoot = newProvider(t, "civil", deathVCT)
	f.civilRegistration = civilProvider.RegistrationCertificate
	taxProvider.VerifierRegistrationCertificate = verifierRegistration(t, "NTRNL-tax")
	f.pidRoot = testpki.NewRoot(t, "pid root")
	pidName := pkix.Name{CommonName: "pid issuer", Organization: []string{"PID provider"}}
	f.pidIssuer = f.pidRoot.Issue(t, testpki.Options{Subject: pidName, EKU: []asn1.ObjectIdentifier{mdlIssuerEKU}})
	f.pidStatus = f.pidRoot.Issue(t, testpki.Options{Subject: pidName, EKU: []asn1.ObjectIdentifier{statusListEKU}})
	f.nlWallet = testwallet.NewProvider(t, "nl wallet provider")
	f.eudiWallet = testwallet.NewProvider(t, "eudi wallet provider")

	var handlers struct{ public, internal http.Handler }
	f.public = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlers.public.ServeHTTP(w, r) }))
	t.Cleanup(f.public.Close)
	f.internal = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlers.internal.ServeHTTP(w, r) }))
	t.Cleanup(f.internal.Close)

	cfg := issuance.Config{
		PublicURL: f.public.URL,
		Providers: []issuance.Provider{taxProvider, civilProvider},
		Profiles: []issuance.Profile{
			{Name: "nl", Kind: issuance.KindNL, WalletProviders: []x5c.Anchors{f.nlWallet.Anchors("nl")},
				UniversalLinkBase: "https://app.example.test/deeplink"},
			{Name: "eudi", Kind: issuance.KindEUDI, WalletProviders: []x5c.Anchors{f.eudiWallet.Anchors("eudi")},
				KeyAttestationProviders: []x5c.Anchors{f.eudiWallet.Anchors("eudi")},
				KeyStorage:              []string{"iso_18045_high"}, UserAuthentication: []string{"iso_18045_high"},
				WalletRedirectURIs: []string{walletRedirect}},
			// One identifier for both wallets: NL Wallets may skip key
			// attestation, EUDI wallets may not.
			{Name: "wallet", Kind: issuance.KindCombined,
				WalletProviders:         []x5c.Anchors{f.eudiWallet.Anchors("eudi"), f.nlWallet.Anchors("nl")},
				PlainJWTWalletProviders: []string{"nl"},
				KeyAttestationProviders: []x5c.Anchors{f.eudiWallet.Anchors("eudi")},
				KeyStorage:              []string{"iso_18045_high"}, UserAuthentication: []string{"iso_18045_high"},
				UniversalLinkBase: "https://app.example.test/deeplink"},
		},
		Configurations: []issuance.CredentialConfiguration{
			{
				ID: incomeConfig, Provider: "tax", VCT: incomeVCT, Scope: "income", ValidDays: 365,
				StatusList: "income",
				Display:    []issuance.Display{{Name: "Income statement", Locale: "en"}},
				Claims: []issuance.Claim{
					{Path: []string{"year"}, Mandatory: true}, {Path: []string{"income"}}, {Path: []string{"address", "city"}},
				},
			},
			{
				ID: deathConfig, Provider: "civil", VCT: deathVCT, Scope: "death", ValidDays: 365,
				StatusList: "death",
				Display:    []issuance.Display{{Name: "Death certificate", Locale: "en"}},
				Claims:     []issuance.Claim{{Path: []string{"name"}, Mandatory: true}},
			},
		},
		StatusListSize: 1024,
		DisclosureOffers: []issuance.DisclosureOffer{{
			Key: incomeOffer, Provider: "tax", ConfigurationIDs: []string{incomeConfig}, Parameters: map[string]string{"jaar": "2025"},
		}},
		PIDAnchors:     []x5c.Anchors{x5c.NewAnchors("test-pid", f.pidRoot.Cert)},
		FetchPIDStatus: f.fetchPIDStatus,
		DisclosureTTL:  disclosureTTL,
		Login:          f.login,
	}
	store := memstore.New()
	svc, err := issuance.New(cfg, &source.Client{HTTP: src.Client(), Endpoints: map[string]string{
		incomeConfig: src.URL + "/attestations/tax/income",
		deathConfig:  src.URL + "/attestations/civil/death",
	}}, store, store, store, f.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(f.logs, nil))
	handlers.public = httpapi.Public(svc, log)
	handlers.internal = httpapi.Internal(svc, log)
	return f
}

// newProvider returns a provider with its own issuer and access roots and a
// registration certificate for vct; the issuer and status certificates come
// from an intermediate.
func newProvider(t *testing.T, name, vct string) (issuance.Provider, testpki.Cert, testpki.Cert) {
	t.Helper()
	subject := pkix.Name{CommonName: name, Organization: []string{name}}
	issuerRoot := testpki.NewRoot(t, name+" issuer root")
	issuerCA := issuerRoot.IssueCA(t, name+" issuer CA")
	issuerCert := issuerCA.Issue(t, testpki.Options{Subject: subject, EKU: []asn1.ObjectIdentifier{mdlIssuerEKU}})
	statusCert := issuerCA.Issue(t, testpki.Options{Subject: subject, EKU: []asn1.ObjectIdentifier{statusListEKU}})
	accessRoot := testpki.NewRoot(t, name+" access root")
	access := accessRoot.Issue(t, testpki.Options{Subject: testpki.OrganizationSubject(name+" access", name, "NTRNL-"+name)})
	registrar := testpki.NewRoot(t, "registrar root").Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "registrar"}})
	return issuance.Provider{
		Name: name, AccessKey: access.Key, AccessChain: access.Chain,
		RegistrationCertificate: testwallet.RegistrationCertificate(t, registrar, "NTRNL-"+name, vct),
		IssuerKey:               issuerCert.Key, IssuerChain: issuerCert.Chain, StatusKey: statusCert.Key, StatusChain: statusCert.Chain,
		RequestEncryptionKey: testpki.NewKey(t),
	}, issuerRoot, accessRoot
}

func (f *fixture) serveSource(w http.ResponseWriter, r *http.Request) {
	f.sourceMu.Lock()
	defer f.sourceMu.Unlock()
	f.sourceCalls = append(f.sourceCalls, r.URL.Query())
	body, _ := io.ReadAll(r.Body)
	f.sourceBodies = append(f.sourceBodies, body)
	if f.sourceStatus != http.StatusOK {
		w.WriteHeader(f.sourceStatus)
		return
	}
	if strings.Contains(r.URL.Path, "/civil/") {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"format": "dc+sd-jwt", "attestation_type": deathVCT,
			"attributes": map[string]any{"name": "A. Example"}}})
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

func (f *fixture) sourceCallCount() int {
	f.sourceMu.Lock()
	defer f.sourceMu.Unlock()
	return len(f.sourceCalls)
}

func (f *fixture) lastSourceBody() []byte {
	f.sourceMu.Lock()
	defer f.sourceMu.Unlock()
	return f.sourceBodies[len(f.sourceBodies)-1]
}

func (f *fixture) lastSourceCall() url.Values {
	f.sourceMu.Lock()
	defer f.sourceMu.Unlock()
	return f.sourceCalls[len(f.sourceCalls)-1]
}

// id is the issuer identifier of the tax provider under profile; idOf that
// of any provider.
func (f *fixture) id(profile string) string { return f.idOf("tax", profile) }

func (f *fixture) idOf(provider, profile string) string {
	return f.public.URL + "/" + provider + "/" + profile
}

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
	f *fixture
	// issuer is the provider the wallet talks to; "tax" unless changed.
	issuer   string
	profile  string
	clientID string
	provider testwallet.Provider
	wiaKey   *ecdsa.PrivateKey
	dpopKey  *ecdsa.PrivateKey
	wia      string
	// header is the response header of the last request.
	header http.Header
}

func (f *fixture) wallet(profile string) *wallet {
	w := &wallet{f: f, issuer: "tax", profile: profile, wiaKey: testpki.NewKey(f.t), dpopKey: testpki.NewKey(f.t)}
	w.clientID, w.provider = nlClient, f.nlWallet
	if profile == "eudi" {
		w.clientID, w.provider = eudiClient, f.eudiWallet
	}
	w.wia = w.provider.WIA(f.t, w.clientID, w.wiaKey, time.Now().Add(time.Hour))
	return w
}

// walletOn is the wallet of kind as ("nl" or "eudi") talking to profile.
func (f *fixture) walletOn(profile, as string) *wallet {
	w := f.wallet(as)
	w.profile = profile
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
		"OAuth-Client-Attestation-PoP": testwallet.PoP(w.f.t, w.wiaKey, w.clientID, w.f.idOf(w.issuer, w.profile), w.challenge()),
		"DPoP":                         testwallet.DPoP(w.f.t, w.dpopKey, "POST", w.f.idOf(w.issuer, w.profile)+"/token", ""),
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
		"DPoP":          testwallet.DPoP(w.f.t, dpopKey, "POST", w.f.idOf(w.issuer, w.profile)+"/credential", accessToken),
	}, "Content-Type", "application/json")
}

func (w *wallet) post(path string, body io.Reader, headers map[string]string, extra ...string) (int, map[string]any) {
	w.f.t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.f.idOf(w.issuer, w.profile)+path, body)
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
	w.header = resp.Header
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
