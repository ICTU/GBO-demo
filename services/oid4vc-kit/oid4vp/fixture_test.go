package oid4vp_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

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
)

// fixture is a verifier with its WRPAC, a PID issuer it trusts (anchor
// "nl") next to a second one ("test-pid"), a holder and a session asking
// for the bsn.
type fixture struct {
	now      time.Time
	pidRoot  testpki.Cert
	issuer   testpki.Cert
	status   testpki.Cert
	testRoot testpki.Cert
	holder   *ecdsa.PrivateKey
	signer   *oid4vp.RequestSigner
	wrpac    testpki.Cert
	session  oid4vp.Session
	tokens   map[string]string
	verifier oid4vp.Verifier
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Now().Truncate(time.Second), holder: testpki.NewKey(t), tokens: map[string]string{}}
	f.pidRoot = testpki.NewRoot(t, "pid root")
	f.issuer, f.status = pidSigners(t, f.pidRoot)
	f.testRoot = testpki.NewRoot(t, "test pid root")
	readerRoot := testpki.NewRoot(t, "reader root")
	f.wrpac = readerRoot.Issue(t, testpki.Options{Subject: testpki.OrganizationSubject("verifier", "Verifier", "NTRNL-99990001")})
	signer, err := oid4vp.NewRequestSigner(f.wrpac.Key, f.wrpac.Chain)
	if err != nil {
		t.Fatal(err)
	}
	f.signer = signer
	key, err := oid4vp.NewResponseKey()
	if err != nil {
		t.Fatal(err)
	}
	f.session = oid4vp.Session{
		ClientID: signer.ClientID(), Nonce: "nonce-1", State: "state-1", Key: key,
		Query: query([]string{"bsn"}),
	}
	f.tokens[statusURI] = f.statusToken(t, f.status, nil)
	f.verifier = oid4vp.Verifier{
		Anchors: []x5c.Anchors{x5c.NewAnchors("nl", f.pidRoot.Cert), x5c.NewAnchors("test-pid", f.testRoot.Cert)},
		FetchStatus: func(_ context.Context, uri string) (string, error) {
			if tok, ok := f.tokens[uri]; ok {
				return tok, nil
			}
			return "", errors.New("unreachable")
		},
	}
	return f
}

// pidName is the distinguished name of a PID issuer's signers.
var pidName = pkix.Name{CommonName: "PID issuer", Organization: []string{"PID provider"}}

// pidSigners returns a PID signer and a status list signer under root,
// with the same name and the extended key usage of their role.
func pidSigners(t *testing.T, root testpki.Cert) (issuer, status testpki.Cert) {
	t.Helper()
	return root.Issue(t, testpki.Options{Subject: pidName, EKU: []asn1.ObjectIdentifier{oid4vp.CredentialSignerEKU}}),
		root.Issue(t, testpki.Options{Subject: pidName, EKU: []asn1.ObjectIdentifier{statuslist.SignerEKU}})
}

func query(paths ...[]string) oid4vp.Query {
	claims := make([]oid4vp.ClaimQuery, len(paths))
	for i, p := range paths {
		claims[i] = oid4vp.ClaimQuery{Path: p}
	}
	return oid4vp.Query{Credentials: []oid4vp.CredentialQuery{{
		ID: "pid", Format: sdjwtvc.Type, Meta: oid4vp.Meta{VCTValues: []string{pidType}}, Claims: claims,
	}}}
}

func (f *fixture) statusToken(t *testing.T, signer testpki.Cert, revoked []int) string {
	t.Helper()
	tok, err := statuslist.Sign(statuslist.Token{URI: statusURI, IssuedAt: f.now, Expires: f.now.Add(time.Hour), Size: 64, Revoked: revoked}, signer.Key, x5c.Header(signer.Chain))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// pidOptions shape the PID the fixture issues.
type pidOptions struct {
	issuer    *testpki.Cert
	vct       string
	notBefore time.Time
	expires   time.Time
	noStatus  bool
	index     int
}

// pid issues a PID with bsn, names and an address, all disclosures in.
func (f *fixture) pid(t *testing.T, o pidOptions) string {
	t.Helper()
	issuer := f.issuer
	if o.issuer != nil {
		issuer = *o.issuer
	}
	if o.vct == "" {
		o.vct = pidType
	}
	if o.notBefore.IsZero() {
		o.notBefore = f.now.Add(-time.Hour)
	}
	if o.expires.IsZero() {
		o.expires = f.now.Add(24 * time.Hour)
	}
	var status *sdjwtvc.StatusReference
	if !o.noStatus {
		status = &sdjwtvc.StatusReference{Index: pidIndex, URI: statusURI}
		if o.index != 0 {
			status.Index = o.index
		}
	}
	s, err := sdjwtvc.NewSigner(issuer.Key, x5c.Header(issuer.Chain))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := s.Issue(sdjwtvc.Credential{
		VCT: o.vct, IssuedAt: f.now, NotBefore: o.notBefore, Expires: o.expires,
		HolderKey: &jose.JSONWebKey{Key: &f.holder.PublicKey}, Status: status,
		Claims: map[string]any{
			"bsn": "999991772", "given_name": "Frouke", "family_name": "Jansen",
			"address": map[string]any{"street_address": "Coolsingel", "locality": "Rotterdam"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cred
}

// present selects the disclosures of keep and binds them to the session.
func (f *fixture) present(t *testing.T, cred string, keep ...string) string {
	t.Helper()
	return testwallet.Present(t, testwallet.Select(t, cred, keep...), f.holder, f.session.ClientID, f.session.Nonce, f.now)
}

// respond encrypts the response body to the session key, as a wallet does.
func (f *fixture) respond(t *testing.T, body any) string {
	t.Helper()
	return f.respondWith(t, body, f.session.Key.PublicJWK(), jose.A128GCM)
}

func (f *fixture) respondWith(t *testing.T, body any, key jose.JSONWebKey, enc jose.ContentEncryption) string {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return testwallet.EncryptRequest(t, raw, key, enc, nil)
}

func (f *fixture) body(presentations ...string) map[string]any {
	return map[string]any{"vp_token": map[string]any{"pid": presentations}, "state": f.session.State}
}

func (f *fixture) verify(compact string) (oid4vp.Disclosure, error) {
	return f.verifier.VerifyResponse(context.Background(), compact, f.session, f.now)
}

// mustRefuse requires the response to be refused for the reason in want.
func (f *fixture) mustRefuse(t *testing.T, name, want, compact string) {
	t.Helper()
	if _, err := f.verify(compact); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: %v, want a refusal with %q", name, err, want)
	}
}

// pidWithoutExp signs, by hand, a PID with a disclosable bsn and no exp,
// which the issuing signer cannot produce.
func (f *fixture) pidWithoutExp(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal([]any{"salt", "bsn", "999991772"})
	if err != nil {
		t.Fatal(err)
	}
	d := base64.RawURLEncoding.EncodeToString(raw)
	return testwallet.Sign(t, f.issuer.Key, sdjwtvc.Type, map[jose.HeaderKey]any{"x5c": x5c.Header(f.issuer.Chain)}, false, map[string]any{
		"_sd": []string{sdjwtvc.Digest(d)}, "_sd_alg": "sha-256", "vct": pidType, "iat": f.now.Unix(),
		"cnf":    map[string]any{"jwk": jose.JSONWebKey{Key: &f.holder.PublicKey}},
		"status": map[string]any{"status_list": map[string]any{"idx": pidIndex, "uri": statusURI}},
	}) + "~" + d + "~"
}

// pidWithoutSdAlg signs, by hand, a valid PID with a disclosable bsn and no
// _sd_alg, which the issuing signer always writes.
func (f *fixture) pidWithoutSdAlg(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal([]any{"salt", "bsn", "999991772"})
	if err != nil {
		t.Fatal(err)
	}
	d := base64.RawURLEncoding.EncodeToString(raw)
	return testwallet.Sign(t, f.issuer.Key, sdjwtvc.Type, map[jose.HeaderKey]any{"x5c": x5c.Header(f.issuer.Chain)}, false, map[string]any{
		"_sd": []string{sdjwtvc.Digest(d)}, "vct": pidType, "iat": f.now.Unix(), "exp": f.now.Add(time.Hour).Unix(),
		"cnf":    map[string]any{"jwk": jose.JSONWebKey{Key: &f.holder.PublicKey}},
		"status": map[string]any{"status_list": map[string]any{"idx": pidIndex, "uri": statusURI}},
	}) + "~" + d + "~"
}
