package oid4vci_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

func TestDecryptRequest(t *testing.T) {
	key := oid4vci.RequestKey{KeyID: "k1", Key: testpki.NewKey(t)}
	keys := []oid4vci.RequestKey{{KeyID: "k0", Key: testpki.NewKey(t)}, key}
	body := []byte(`{"credential_identifier":"c1"}`)

	for _, enc := range oid4vci.EncryptionMethods {
		got, err := oid4vci.DecryptRequest(testwallet.EncryptRequest(t, body, key.PublicJWK(), enc, nil), keys)
		if err != nil || string(got) != string(body) {
			t.Fatalf("%s: %q %v", enc, got, err)
		}
	}
	// The EUDI reference wallet also puts the issuer's key in a jwk header.
	withJWK := testwallet.EncryptRequest(t, body, key.PublicJWK(), jose.A256GCM,
		(&jose.EncrypterOptions{}).WithHeader("jwk", key.PublicJWK()))
	if got, err := oid4vci.DecryptRequest(withJWK, keys); err != nil || string(got) != string(body) {
		t.Fatalf("with jwk header: %q %v", got, err)
	}
	noKid := testwallet.EncryptRequest(t, body, oid4vci.RequestKey{Key: key.Key}.PublicJWK(), jose.A256GCM, nil)
	if _, err := oid4vci.DecryptRequest(noKid, []oid4vci.RequestKey{{Key: key.Key}}); err == nil {
		t.Fatal("a key without kid must never match")
	}
	refused := map[string]string{
		"unknown kid": testwallet.EncryptRequest(t, body, oid4vci.RequestKey{KeyID: "other", Key: testpki.NewKey(t)}.PublicJWK(), jose.A256GCM, nil),
		"wrong key":   testwallet.EncryptRequest(t, body, oid4vci.RequestKey{KeyID: "k1", Key: testpki.NewKey(t)}.PublicJWK(), jose.A256GCM, nil),
		"compressed":  testwallet.EncryptRequest(t, body, key.PublicJWK(), jose.A256GCM, &jose.EncrypterOptions{Compression: jose.DEFLATE}),
		"A192GCM":     testwallet.EncryptRequest(t, body, key.PublicJWK(), jose.A192GCM, nil),
		"signed JWT":  testwallet.JWTProof(t, testpki.NewKey(t), "aud", "n", ""),
	}
	for name, req := range refused {
		if _, err := oid4vci.DecryptRequest(req, keys); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestResponseEncryption(t *testing.T) {
	walletKey, params := testwallet.ResponseEncryption(t, "A256GCM")
	e, err := oid4vci.NewResponseEncrypter(params)
	if err != nil {
		t.Fatal(err)
	}
	jwe, err := oid4vci.EncryptResponse(e, []byte(`{"credentials":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	h, body := testwallet.DecryptResponse(t, jwe, walletKey)
	if string(body) != `{"credentials":[]}` || h.KeyID != "wallet-enc" || h.ExtraHeaders["enc"] != "A256GCM" {
		t.Fatalf("header %+v body %s", h, body)
	}

	jwk := func(k any, alg string) json.RawMessage {
		raw, err := json.Marshal(jose.JSONWebKey{Key: k, Algorithm: alg})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// A key as the EUDI reference wallet sends it: no kid needed, an iat
	// member that is not part of RFC 7517.
	var walletJWK map[string]any
	if err := json.Unmarshal(jwk(&testpki.NewKey(t).PublicKey, "ECDH-ES"), &walletJWK); err != nil {
		t.Fatal(err)
	}
	walletJWK["use"], walletJWK["iat"] = "enc", 1767225600
	raw, _ := json.Marshal(walletJWK)
	if _, err := oid4vci.NewResponseEncrypter(oid4vci.ResponseEncryption{JWK: raw, Enc: "A128GCM"}); err != nil {
		t.Fatalf("a wallet key with iat and without kid: %v", err)
	}
	walletJWK["use"] = "sig"
	signingKey, _ := json.Marshal(walletJWK)
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private := testpki.NewKey(t)
	refused := map[string]oid4vci.ResponseEncryption{
		"zip":         {JWK: params.JWK, Enc: "A256GCM", Zip: "DEF"},
		"A192GCM":     {JWK: params.JWK, Enc: "A192GCM"},
		"no alg":      {JWK: jwk(&private.PublicKey, ""), Enc: "A128GCM"},
		"other alg":   {JWK: jwk(&private.PublicKey, "ECDH-ES+A128KW"), Enc: "A128GCM"},
		"P-384":       {JWK: jwk(&p384.PublicKey, "ECDH-ES"), Enc: "A128GCM"},
		"private key": {JWK: jwk(private, "ECDH-ES"), Enc: "A128GCM"},
		"no jwk":      {Enc: "A128GCM"},
		"signing key": {JWK: signingKey, Enc: "A128GCM"},
	}
	for name, p := range refused {
		if _, err := oid4vci.NewResponseEncrypter(p); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}
