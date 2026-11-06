package keyattest_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/keyattest"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

// signedWith signs claims as a compact JWS with alg and key, with exactly
// the given header parameters besides alg. Alg "none" gives an unsecured
// JWS (RFC 7515 Appendix A.5).
func signedWith(t *testing.T, alg jose.SignatureAlgorithm, key any, header map[jose.HeaderKey]any, claims map[string]any) string {
	t.Helper()
	if alg == "none" {
		h := map[string]any{"alg": "none"}
		for k, v := range header {
			h[string(k)] = v
		}
		return b64JSON(t, h) + "." + b64JSON(t, claims) + "."
	}
	opts := &jose.SignerOptions{}
	for k, v := range header {
		opts = opts.WithHeader(k, v)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, opts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func b64JSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// p384Signer returns a P-384 signing certificate under the provider's
// root, so a key attestation signed with ES384 has a chain that verifies.
func p384Signer(t *testing.T, p testwallet.Provider) (*ecdsa.PrivateKey, []*x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "P-384 key attestation signer"},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.Root.Cert, &key.PublicKey, p.Root.Key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, []*x509.Certificate{cert}
}

// Appendix D.1, F.3 and F.1 (key_attestation): alg, typ and iat of a key
// attestation are checked on their own. Each refusal changes one thing in
// an otherwise valid attestation with a chain that verifies, and the error
// names the check that refused it.
func TestKeyAttestationConformance(t *testing.T) {
	provider := testwallet.NewProvider(t, "provider")
	anchors := []x5c.Anchors{provider.Anchors("p")}
	now := time.Now()
	attested := []jose.JSONWebKey{{Key: &testpki.NewKey(t).PublicKey}}
	header := func(typ any, chain []*x509.Certificate) map[jose.HeaderKey]any {
		h := map[jose.HeaderKey]any{"x5c": x5c.Header(chain)}
		if typ != nil {
			h[jose.HeaderType] = typ
		}
		return h
	}
	claims := func(drop string) map[string]any {
		c := map[string]any{"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "attested_keys": attested, "nonce": "n"}
		delete(c, drop)
		return c
	}
	signerKey, signerChain := provider.Signer.Key, provider.Signer.Chain

	if v, err := keyattest.Verify(signedWith(t, jose.ES256, signerKey, header(keyattest.Type, signerChain), claims("")), anchors, now, true); err != nil || v.Nonce != "n" {
		t.Fatalf("the valid attestation the cases start from: %+v %v", v, err)
	}

	p384, p384Chain := p384Signer(t, provider)
	if _, err := p384Chain[0].Verify(x509.VerifyOptions{Roots: anchors[0].Roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Fatalf("the P-384 signer must chain to the provider: %v", err)
	}
	for name, tc := range map[string]struct {
		attestation string
		want        string // in the error
	}{
		// D.1-01, D.1-02, F.3-04, F.1-22: alg is ES256, the one value of
		// proof_signing_alg_values_supported; never none or a MAC.
		"F.3-04 ES384 with a chain that verifies": {signedWith(t, jose.ES384, p384, header(keyattest.Type, p384Chain), claims("")), "ES256"},
		"D.1-02 MAC":  {signedWith(t, jose.HS256, []byte("a shared secret of thirty-two by"), header(keyattest.Type, signerChain), claims("")), "ES256"},
		"D.1-02 none": {signedWith(t, "none", nil, header(keyattest.Type, signerChain), claims("")), "ES256"},
		// D.1-03, D.1-04: typ is required and is key-attestation+jwt.
		"D.1-03 no typ":         {signedWith(t, jose.ES256, signerKey, header(nil, signerChain), claims("")), "typ"},
		"D.1-04 typ jwt":        {signedWith(t, jose.ES256, signerKey, header("jwt", signerChain), claims("")), "typ"},
		"D.1-04 typ of a proof": {signedWith(t, jose.ES256, signerKey, header("openid4vci-proof+jwt", signerChain), claims("")), "typ"},
		// D.1-05: iat is required.
		"D.1-05 no iat": {signedWith(t, jose.ES256, signerKey, header(keyattest.Type, signerChain), claims("iat")), "iat"},
	} {
		t.Run(name, func(t *testing.T) {
			// With the attestation proof type exp is optional; the
			// refusals hold there too.
			for _, requireExp := range []bool{true, false} {
				_, err := keyattest.Verify(tc.attestation, anchors, now, requireExp)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("requireExp %v: got %v, want a refusal about %q", requireExp, err, tc.want)
				}
			}
		})
	}
}
