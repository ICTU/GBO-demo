package oid4vci_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
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

// Appendix F.1 and F.4: every element of a jwt key proof is checked on its
// own. Each refusal changes one thing in an otherwise valid proof, and the
// error names the check that refused it.
func TestJWTProofConformance(t *testing.T) {
	key := testpki.NewKey(t)
	other := testpki.NewKey(t)
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := testpki.NewRoot(t, "proof root").Issue(t, testpki.Options{Subject: pkix.Name{CommonName: "proof key"}})
	now := time.Now()
	pub := func(k *ecdsa.PrivateKey) jose.JSONWebKey { return jose.JSONWebKey{Key: &k.PublicKey} }
	header := func(extra map[jose.HeaderKey]any) map[jose.HeaderKey]any {
		h := map[jose.HeaderKey]any{jose.HeaderType: oid4vci.ProofType, "jwk": pub(key)}
		for k, v := range extra {
			if v == nil {
				delete(h, k)
			} else {
				h[k] = v
			}
		}
		return h
	}
	claims := func(drop string) map[string]any {
		c := map[string]any{"aud": issuer, "iat": now.Unix(), "nonce": "n1"}
		delete(c, drop)
		return c
	}

	if p, err := oid4vci.VerifyJWTProof(signedWith(t, jose.ES256, key, header(nil), claims("")), issuer, now); err != nil || p.Nonce != "n1" {
		t.Fatalf("the valid proof the cases start from: %+v %v", p, err)
	}

	// C-1, C-2 (RFC 7515 section 4.1.9): typ in another case, or with the
	// application/ prefix, is the same media type.
	for _, typ := range []string{"OpenID4VCI-Proof+JWT", "application/openid4vci-proof+jwt"} {
		if _, err := oid4vci.VerifyJWTProof(signedWith(t, jose.ES256, key, header(map[jose.HeaderKey]any{jose.HeaderType: typ}), claims("")), issuer, now); err != nil {
			t.Errorf("typ %s: %v", typ, err)
		}
	}

	for name, tc := range map[string]struct {
		proof string
		want  string // in the error
	}{
		// F.1-02, F.1-03, F.1-22: alg is ES256, the one value of
		// proof_signing_alg_values_supported; never none or a MAC.
		"F.1-22 ES384": {signedWith(t, jose.ES384, p384, header(map[jose.HeaderKey]any{"jwk": pub(p384)}), claims("")), "ES256"},
		"F.1-03 MAC":   {signedWith(t, jose.HS256, []byte("a shared secret of thirty-two by"), header(nil), claims("")), "ES256"},
		"F.1-03 none":  {signedWith(t, "none", nil, header(nil), claims("")), "ES256"},
		// F.1-04, F.1-05: typ is required and is openid4vci-proof+jwt.
		"F.1-04 no typ":              {signedWith(t, jose.ES256, key, header(map[jose.HeaderKey]any{jose.HeaderType: nil}), claims("")), "typ"},
		"F.1-05 typ jwt":             {signedWith(t, jose.ES256, key, header(map[jose.HeaderKey]any{jose.HeaderType: "jwt"}), claims("")), "typ"},
		"F.1-05 typ key-attestation": {signedWith(t, jose.ES256, key, header(map[jose.HeaderKey]any{jose.HeaderType: "key-attestation+jwt"}), claims("")), "typ"},
		// F.1-06 to F.1-08: one of kid, jwk and x5c; this issuer takes jwk
		// only, so kid and x5c are refused, alone or with another.
		"F.1-06 F.1-07 jwk and kid": {signedWith(t, jose.ES256, key, header(map[jose.HeaderKey]any{"kid": "key-1"}), claims("")), "jwk"},
		"F.1-06 kid alone":          {signedWith(t, jose.ES256, key, header(map[jose.HeaderKey]any{"jwk": nil, "kid": "key-1"}), claims("")), "jwk"},
		"F.1-08 x5c and kid": {signedWith(t, jose.ES256, cert.Key, header(map[jose.HeaderKey]any{
			"jwk": nil, "kid": "key-1", "x5c": x5c.Header(cert.Chain)}), claims("")), "jwk"},
		"F.1-07 F.1-08 jwk and a valid x5c": {signedWith(t, jose.ES256, cert.Key, header(map[jose.HeaderKey]any{
			"jwk": pub(cert.Key), "x5c": x5c.Header(cert.Chain)}), claims("")), "jwk"},
		"F.4 no key at all": {signedWith(t, jose.ES256, key, header(map[jose.HeaderKey]any{"jwk": nil}), claims("")), "jwk"},
		// F.1-19, F.4: the signature verifies with the key in the header.
		"F.1-19 signed by another key than the jwk": {signedWith(t, jose.ES256, other, header(nil), claims("")), "signature"},
		// F.1-15: iat is required; aud is checked as well (F.1-01).
		"F.1-15 no iat": {signedWith(t, jose.ES256, key, header(nil), claims("iat")), "iat"},
		"F.1-01 no aud": {signedWith(t, jose.ES256, key, header(nil), claims("aud")), "another credential issuer"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := oid4vci.VerifyJWTProof(tc.proof, issuer, now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a refusal about %q", err, tc.want)
			}
		})
	}
}

// F.4: a jwk header that holds a private key is refused. go-jose already
// refuses such a header while parsing, so the refusal names no particular
// check.
func TestJWTProofWithAPrivateJWK(t *testing.T) {
	key := testpki.NewKey(t)
	now := time.Now()
	proof := signedWith(t, jose.ES256, key, map[jose.HeaderKey]any{jose.HeaderType: oid4vci.ProofType, "jwk": jose.JSONWebKey{Key: key}},
		map[string]any{"aud": issuer, "iat": now.Unix(), "nonce": "n1"})
	if !strings.Contains(string(mustDecodeHeader(t, proof)), `"d":`) {
		t.Fatal("the test proof must carry the private part of its key")
	}
	if _, err := oid4vci.VerifyJWTProof(proof, issuer, now); err == nil {
		t.Fatal("a proof with a private jwk must be refused")
	}
}

func mustDecodeHeader(t *testing.T, compact string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.SplitN(compact, ".", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
