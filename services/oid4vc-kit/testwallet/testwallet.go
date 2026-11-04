// Package testwallet builds the messages a wallet and its wallet provider
// send to a credential issuer or a verifier, for tests only.
package testwallet

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/dpop"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/x5c"
)

// Provider is a wallet provider that signs Wallet Instance Attestations
// and key attestations.
type Provider struct {
	Root   testpki.Cert
	Signer testpki.Cert
}

// NewProvider returns a provider with its own root.
func NewProvider(t testing.TB, name string) Provider {
	t.Helper()
	root := testpki.NewRoot(t, name+" root")
	return Provider{Root: root, Signer: root.Issue(t, testpki.Options{})}
}

// Anchors returns the provider's root as a trust anchor set.
func (p Provider) Anchors(name string) x5c.Anchors {
	return x5c.NewAnchors(name, p.Root.Cert)
}

// WIA returns a Wallet Instance Attestation for clientID binding key.
func (p Provider) WIA(t testing.TB, clientID string, key *ecdsa.PrivateKey, exp time.Time) string {
	t.Helper()
	return Sign(t, p.Signer.Key, "oauth-client-attestation+jwt", map[jose.HeaderKey]any{"x5c": x5c.Header(p.Signer.Chain)}, false, map[string]any{
		"sub": clientID,
		"iat": time.Now().Unix(),
		"exp": exp.Unix(),
		"cnf": map[string]any{"jwk": jose.JSONWebKey{Key: &key.PublicKey}},
	})
}

// KeyAttestation returns a key attestation for keys with nonce, asserting
// iso_18045_high for key storage and user authentication.
func (p Provider) KeyAttestation(t testing.TB, keys []*ecdsa.PrivateKey, nonce string, exp time.Time) string {
	t.Helper()
	return p.KeyAttestationClaims(t, keys, map[string]any{
		"exp": exp.Unix(), "nonce": nonce,
		"key_storage": []string{"iso_18045_high"}, "user_authentication": []string{"iso_18045_high"},
	})
}

// KeyAttestationClaims returns a key attestation for keys with iat and the
// given claims.
func (p Provider) KeyAttestationClaims(t testing.TB, keys []*ecdsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	attested := make([]jose.JSONWebKey, len(keys))
	for i, k := range keys {
		attested[i] = jose.JSONWebKey{Key: &k.PublicKey}
	}
	all := map[string]any{"iat": time.Now().Unix(), "attested_keys": attested}
	for k, v := range claims {
		all[k] = v
	}
	return Sign(t, p.Signer.Key, "key-attestation+jwt", map[jose.HeaderKey]any{"x5c": x5c.Header(p.Signer.Chain)}, false, all)
}

// PoP returns a client attestation PoP signed with the attested key.
func PoP(t testing.TB, key *ecdsa.PrivateKey, clientID, audience, challenge string) string {
	t.Helper()
	claims := map[string]any{"iss": clientID, "aud": audience, "jti": random(t), "iat": time.Now().Unix()}
	if challenge != "" {
		claims["challenge"] = challenge
	}
	return Sign(t, key, "oauth-client-attestation-pop+jwt", nil, false, claims)
}

// DPoP returns a DPoP proof; accessToken, when set, is bound through ath.
func DPoP(t testing.TB, key *ecdsa.PrivateKey, method, url, accessToken string) string {
	t.Helper()
	claims := map[string]any{"jti": random(t), "htm": method, "htu": url, "iat": time.Now().Unix()}
	if accessToken != "" {
		claims["ath"] = dpop.AccessTokenHash(accessToken)
	}
	return Sign(t, key, "dpop+jwt", nil, true, claims)
}

// JWTProof returns a jwt key proof; keyAttestation, when set, goes in the
// key_attestation header.
func JWTProof(t testing.TB, key *ecdsa.PrivateKey, audience, nonce, keyAttestation string) string {
	t.Helper()
	var extra map[jose.HeaderKey]any
	if keyAttestation != "" {
		extra = map[jose.HeaderKey]any{"key_attestation": keyAttestation}
	}
	return Sign(t, key, "openid4vci-proof+jwt", extra, true, map[string]any{
		"aud": audience, "iat": time.Now().Unix(), "nonce": nonce,
	})
}

// Sign signs claims with key as an ES256 JWT of type typ.
func Sign(t testing.TB, key *ecdsa.PrivateKey, typ string, extra map[jose.HeaderKey]any, embedJWK bool, claims map[string]any) string {
	t.Helper()
	opts := (&jose.SignerOptions{EmbedJWK: embedJWK}).WithType(jose.ContentType(typ))
	for k, v := range extra {
		opts = opts.WithHeader(k, v)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
	if err != nil {
		t.Fatal(err)
	}
	s, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// ResponseEncryption returns a wallet key for an encrypted credential
// response and the credential_response_encryption object that asks for it.
func ResponseEncryption(t testing.TB, enc string) (*ecdsa.PrivateKey, oid4vci.ResponseEncryption) {
	t.Helper()
	key := testpki.NewKey(t)
	raw, err := json.Marshal(jose.JSONWebKey{Key: &key.PublicKey, KeyID: "wallet-enc", Algorithm: string(oid4vci.EncryptionAlgorithm), Use: "enc"})
	if err != nil {
		t.Fatal(err)
	}
	return key, oid4vci.ResponseEncryption{JWK: raw, Enc: enc}
}

// EncryptRequest encrypts a credential request body to the issuer's key,
// as the wallet does (OpenID4VCI 1.0 section 10).
func EncryptRequest(t testing.TB, body []byte, issuerKey jose.JSONWebKey, enc jose.ContentEncryption, opts *jose.EncrypterOptions) string {
	t.Helper()
	e, err := jose.NewEncrypter(enc, jose.Recipient{Algorithm: oid4vci.EncryptionAlgorithm, Key: issuerKey.Key, KeyID: issuerKey.KeyID}, opts)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := e.Encrypt(body)
	if err != nil {
		t.Fatal(err)
	}
	s, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// EncryptWith encrypts body to key with the given key management
// algorithm, for tests of what a recipient refuses.
func EncryptWith(t testing.TB, body []byte, key jose.JSONWebKey, alg jose.KeyAlgorithm, enc jose.ContentEncryption) string {
	t.Helper()
	e, err := jose.NewEncrypter(enc, jose.Recipient{Algorithm: alg, Key: key.Key, KeyID: key.KeyID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := e.Encrypt(body)
	if err != nil {
		t.Fatal(err)
	}
	s, err := obj.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// DecryptResponse decrypts an encrypted credential response with the
// wallet's key and returns its header and body.
func DecryptResponse(t testing.TB, compact string, key *ecdsa.PrivateKey) (jose.Header, []byte) {
	t.Helper()
	obj, err := jose.ParseEncryptedCompact(compact, []jose.KeyAlgorithm{oid4vci.EncryptionAlgorithm}, oid4vci.EncryptionMethods)
	if err != nil {
		t.Fatal(err)
	}
	body, err := obj.Decrypt(key)
	if err != nil {
		t.Fatal(err)
	}
	return obj.Header, body
}

func random(t testing.TB) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// RegistrationCertificate returns a WRPRC (ETSI TS 119 475) in JWT form for
// subject sub that provides the given vcts, signed by registrar.
func RegistrationCertificate(t testing.TB, registrar testpki.Cert, sub string, vcts ...string) []byte {
	t.Helper()
	provides := make([]map[string]any, 0, len(vcts))
	for _, v := range vcts {
		provides = append(provides, map[string]any{"format": "dc+sd-jwt", "meta": map[string]any{"vct_values": []string{v}}})
	}
	return []byte(Sign(t, registrar.Key, "rc-wrp+jwt", map[jose.HeaderKey]any{"x5c": x5c.Header(registrar.Chain)}, false, map[string]any{
		"sub": sub, "sub_ln": sub, "country": "NL", "iat": time.Now().Unix(), "provides_attestations": provides,
	}))
}

// Select returns credential, in combined format, with only the
// disclosures of claims named in keep, at any depth.
func Select(t testing.TB, credential string, keep ...string) string {
	t.Helper()
	parts := strings.Split(credential, "~")
	out := parts[0] + "~"
	for _, d := range parts[1 : len(parts)-1] {
		raw, err := base64.RawURLEncoding.DecodeString(d)
		if err != nil {
			t.Fatal(err)
		}
		var arr []any
		if err := json.Unmarshal(raw, &arr); err != nil {
			t.Fatal(err)
		}
		if name, _ := arr[1].(string); slices.Contains(keep, name) {
			out += d + "~"
		}
	}
	return out
}

// SDHash is the sd_hash of an SD-JWT in combined format (RFC 9901 section
// 4.3.1).
func SDHash(sdjwt string) string {
	sum := sha256.Sum256([]byte(sdjwt))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Present returns the SD-JWT+KB of sdjwt bound with key to aud and nonce at
// iat, as a wallet presents it.
func Present(t testing.TB, sdjwt string, key *ecdsa.PrivateKey, aud, nonce string, iat time.Time) string {
	t.Helper()
	return sdjwt + Sign(t, key, "kb+jwt", nil, false, map[string]any{
		"aud": aud, "nonce": nonce, "iat": iat.Unix(), "sd_hash": SDHash(sdjwt),
	})
}
