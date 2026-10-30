// Package interop drives the credential issuer with an independent wallet:
// OID4VCgo (github.com/idfoundry/oid4vcgo, MIT), OpenID Certified for the
// OpenID4VCI 1.0 + HAIP 1.0 wallet role. The tests in internal/httpapi use a
// wallet written alongside the issuer; these use one that was not, so a
// shared misreading of the specifications does not pass unnoticed.
//
// It is a separate module because OID4VCgo needs a newer Go than the issuer
// module (go 1.26.10, see go.mod); the issuer module and its CI are not
// affected. Run it with `go test ./...` in this directory.
package interop_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	oid4vci "github.com/idfoundry/oid4vcgo"
	"github.com/idfoundry/oid4vcgo/attestation"

	"gbo-demo/eudi-credential-issuer/internal/httpapi"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/eudi-credential-issuer/internal/source"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/x5c"
)

var (
	mdlIssuerEKU  = asn1.ObjectIdentifier{1, 0, 18013, 5, 1, 2}
	statusListEKU = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 127}
)

const (
	incomeConfig = "income"
	incomeVCT    = "https://source.example.test/types/income/v1"
	clientID     = "https://interop-wallet.example.test"
	providerID   = "https://interop-wallet-provider.example.test"
)

// issuer is a running credential issuer with the nl and eudi profiles, as
// internal/httpapi's fixture builds it, except that both profiles trust
// the OID4VCgo-side wallet provider below.
type issuer struct {
	t          *testing.T
	public     *httptest.Server
	internal   *httptest.Server
	issuerRoot testpki.Cert
	accessRoot testpki.Cert
	provider   *provider

	mu   sync.Mutex
	seen []exchange
}

// exchange is what the wallet sent to the public port, as far as the
// tests check it: the endpoint, the response status, and for the token
// and credential endpoints the parts that show which choices the wallet
// made.
type exchange struct {
	Method, Path string
	Status       int
	// PoPChallenge: the token request's attestation PoP carries a
	// challenge claim.
	PoPChallenge bool
	// DPoP: the request carries a DPoP header.
	DPoP bool
	// ProofTypes are the keys of the credential request's proofs, and
	// JWTKeyAttestation whether its first jwt proof has a key_attestation
	// header.
	ProofTypes        []string
	JWTProofs         int
	JWTKeyAttestation bool
	Accept            string
}

// record wraps the public handler and keeps an exchange per request.
func (is *issuer) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		e := exchange{Method: r.Method, Path: r.URL.Path, DPoP: r.Header.Get("DPoP") != "", Accept: r.Header.Get("Accept")}
		if pop := r.Header.Get("OAuth-Client-Attestation-PoP"); pop != "" {
			_, ok := jwtPart(pop, 1)["challenge"]
			e.PoPChallenge = ok
		}
		if strings.HasSuffix(r.URL.Path, "/credential") {
			var req struct {
				Proofs map[string][]string `json:"proofs"`
			}
			_ = json.Unmarshal(body, &req)
			for k := range req.Proofs {
				e.ProofTypes = append(e.ProofTypes, k)
			}
			e.JWTProofs = len(req.Proofs["jwt"])
			if e.JWTProofs > 0 {
				_, e.JWTKeyAttestation = jwtPart(req.Proofs["jwt"][0], 0)["key_attestation"]
			}
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		e.Status = rec.status
		is.mu.Lock()
		is.seen = append(is.seen, e)
		is.mu.Unlock()
	})
}

// exchanges returns the recorded exchanges whose path ends in suffix.
func (is *issuer) exchanges(suffix string) []exchange {
	is.mu.Lock()
	defer is.mu.Unlock()
	var out []exchange
	for _, e := range is.seen {
		if strings.HasSuffix(e.Path, suffix) {
			out = append(out, e)
		}
	}
	return out
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// jwtPart decodes part i (0 header, 1 payload) of a compact JWT, without
// verifying it.
func jwtPart(token string, i int) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[i])
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// newIssuer starts the issuer; opts change its configuration first.
func newIssuer(t *testing.T, opts ...func(*issuance.Config)) *issuer {
	t.Helper()
	is, err := tryIssuer(t, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return is
}

// tryIssuer is newIssuer that returns the error of issuance.New.
func tryIssuer(t *testing.T, opts ...func(*issuance.Config)) (*issuer, error) {
	t.Helper()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": "doc-1", "format": "dc+sd-jwt", "attestation_type": incomeVCT,
			"attributes": map[string]any{"year": "2025", "income": 41000, "address": map[string]any{"city": "Utrecht"}},
		}})
	}))
	t.Cleanup(src.Close)

	is := &issuer{t: t, provider: newProvider(t)}
	subject := pkix.Name{CommonName: "Issuer", Organization: []string{"GBO Demo"}}
	is.issuerRoot = testpki.NewRoot(t, "issuer root")
	issuerCA := is.issuerRoot.IssueCA(t, "issuer CA")
	issuerCert := issuerCA.Issue(t, testpki.Options{Subject: subject, EKU: []asn1.ObjectIdentifier{mdlIssuerEKU}})
	statusCert := issuerCA.Issue(t, testpki.Options{Subject: subject, EKU: []asn1.ObjectIdentifier{statusListEKU}})
	is.accessRoot = testpki.NewRoot(t, "access root")
	access := is.accessRoot.Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "access", Organization: []string{"GBO Demo"}}})

	var handlers struct{ public, internal http.Handler }
	// The public port over TLS: walletflow's Development mode allows
	// loopback hosts but still requires https (fapihttp.ErrInsecureURL).
	is.public = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlers.public.ServeHTTP(w, r) }))
	t.Cleanup(is.public.Close)
	is.internal = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlers.internal.ServeHTTP(w, r) }))
	t.Cleanup(is.internal.Close)

	providers := []x5c.Anchors{x5c.NewAnchors("oid4vcgo", is.provider.root.Cert)}
	cfg := issuance.Config{
		PublicURL: is.public.URL,
		Profiles: []issuance.Profile{
			{Name: "nl", Kind: issuance.KindNL, WalletProviders: providers,
				AccessKey: access.Key, AccessChain: access.Chain, RegistrationCertificate: []byte("wrprc"),
				UniversalLinkBase: "https://app.example.test/deeplink"},
			{Name: "eudi", Kind: issuance.KindEUDI, WalletProviders: providers, KeyAttestationProviders: providers,
				KeyStorage: []string{"iso_18045_high"}, UserAuthentication: []string{"iso_18045_high"},
				AccessKey: access.Key, AccessChain: access.Chain},
		},
		Configurations: []issuance.CredentialConfiguration{{
			ID: incomeConfig, VCT: incomeVCT, Scope: "income", ValidDays: 365,
			StatusList: "income",
			Display:    []issuance.Display{{Name: "Income statement", Locale: "en"}},
			Claims: []issuance.Claim{
				{Path: []string{"year"}, Mandatory: true}, {Path: []string{"income"}}, {Path: []string{"address", "city"}},
			},
		}},
		IssuerKey: issuerCert.Key, IssuerChain: issuerCert.Chain,
		StatusKey: statusCert.Key, StatusChain: statusCert.Chain,
		StatusListSize: 1024,
	}
	for _, o := range opts {
		o(&cfg)
	}
	store := memstore.New()
	svc, err := issuance.New(cfg, &source.Client{HTTP: src.Client(), Endpoints: map[string]string{incomeConfig: src.URL + "/attestations/tax/income"}}, store, store, store, time.Now)
	if err != nil {
		return nil, err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handlers.public = is.record(httpapi.Public(svc, log))
	handlers.internal = httpapi.Internal(svc, log)
	return is, nil
}

// id is the credential issuer identifier of a profile.
func (is *issuer) id(profile string) string { return is.public.URL + "/" + profile }

// offer creates an offer for the income configuration on the internal API
// and returns the offer links by kind.
func (is *issuer) offer(profile string) map[string]string {
	is.t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"profile": profile, "credential_configuration_ids": []string{incomeConfig},
		"subject": map[string]any{"bsn": "999990019"}, "parameters": map[string]string{"jaar": "2025"},
	})
	resp, err := http.Post(is.internal.URL+"/offers", "application/json", bytes.NewReader(raw))
	if err != nil {
		is.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Links map[string]string `json:"links"`
	}
	if resp.StatusCode != http.StatusCreated || json.NewDecoder(resp.Body).Decode(&body) != nil {
		is.t.Fatalf("offer: status %d", resp.StatusCode)
	}
	return body.Links
}

// httpClient is an HTTP client that trusts the public port's TLS
// certificate (httptest's own CA), for the wallet.
func (is *issuer) httpClient() *http.Client { return is.public.Client() }

func (is *issuer) issuerRoots() *x509.CertPool { return pool(is.issuerRoot.Cert) }
func (is *issuer) accessRoots() *x509.CertPool { return pool(is.accessRoot.Cert) }

func pool(certs ...*x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	for _, c := range certs {
		p.AddCert(c)
	}
	return p
}

// provider is the wallet provider on the wallet's side. Its certificates
// come from testpki (plain X.509), but the Wallet Attestations and Key
// Attestations are built by OID4VCgo's own attestation package, not by
// oid4vc-kit/testwallet. It implements walletflow.WalletProvider.
type provider struct {
	root   testpki.Cert
	signer testpki.Cert
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	root := testpki.NewRoot(t, "oid4vcgo wallet provider root")
	return &provider{root: root, signer: root.Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "oid4vcgo wallet provider"}})}
}

func (p *provider) header() attestation.Header {
	return attestation.Header{X5C: []string{base64.StdEncoding.EncodeToString(p.signer.Cert.Raw)}}
}

// WalletAttestation implements walletflow.WalletProvider.
func (p *provider) WalletAttestation(_ context.Context, clientID string, instanceKey crypto.PublicKey) (string, error) {
	now := time.Now()
	return attestation.IssueWalletAttestation(p.signer.Key, oid4vci.ES256, p.header(), attestation.WalletAttestationClaims{
		Issuer: providerID, Subject: clientID, InstanceKey: instanceKey,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
	})
}

// KeyAttestation implements walletflow.WalletProvider.
func (p *provider) KeyAttestation(_ context.Context, keys []*ecdsa.PublicKey, nonce string) (string, error) {
	claims, err := p.keyAttestationClaims(keys)
	if err != nil {
		return "", err
	}
	claims.IssuedAt, claims.Nonce = time.Now().Unix(), nonce
	return attestation.Issue(p.signer.Key, oid4vci.ES256, p.header(), claims)
}

// keyAttestationClaims are the claims of a key attestation for keys,
// without iat and nonce; exp is required for the jwt proof type
// (Appendix D.1).
func (p *provider) keyAttestationClaims(keys []*ecdsa.PublicKey) (attestation.Claims, error) {
	attested := make([]json.RawMessage, 0, len(keys))
	for _, k := range keys {
		raw, err := attestation.AttestedKey(k)
		if err != nil {
			return attestation.Claims{}, err
		}
		attested = append(attested, raw)
	}
	exp := time.Now().Add(time.Hour).Unix()
	high := []attestation.AttackPotentialResistance{attestation.ISO18045High}
	return attestation.Claims{Issuer: providerID, ExpiresAt: &exp, AttestedKeys: attested,
		KeyStorage: high, UserAuthentication: high}, nil
}
