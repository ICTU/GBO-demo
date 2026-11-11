package oid4vci_test

import (
	"crypto/ecdsa"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/keyattest"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

const issuer = "https://issuer.example.test/eudi"

func TestVerifyJWTProof(t *testing.T) {
	key := testpki.NewKey(t)
	now := time.Now()
	p, err := oid4vci.VerifyJWTProof(testwallet.JWTProof(t, key, issuer, "n1", ""), issuer, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.Nonce != "n1" || p.KeyAttestation != "" {
		t.Fatalf("unexpected proof %+v", p)
	}
	if _, err := oid4vci.VerifyJWTProof(testwallet.JWTProof(t, key, "https://elsewhere.test", "n1", ""), issuer, now); err == nil {
		t.Fatal("a proof for another issuer must be refused")
	}
	if _, err := oid4vci.VerifyJWTProof(testwallet.JWTProof(t, key, issuer, "n1", ""), issuer, now.Add(10*time.Minute)); err == nil {
		t.Fatal("an old proof must be refused")
	}
	withX5c := testwallet.Sign(t, key, oid4vci.ProofType, map[jose.HeaderKey]any{"x5c": []string{"AA=="}}, true,
		map[string]any{"aud": issuer, "iat": now.Unix(), "nonce": "n1"})
	if _, err := oid4vci.VerifyJWTProof(withX5c, issuer, now); err == nil {
		t.Fatal("a proof with both jwk and x5c must be refused")
	}
}

func TestProofWithKeyAttestation(t *testing.T) {
	provider := testwallet.NewProvider(t, "eudi provider")
	keys := []*ecdsa.PrivateKey{testpki.NewKey(t), testpki.NewKey(t)}
	now := time.Now()
	ka := provider.KeyAttestation(t, keys, "n2", now.Add(time.Hour))
	p, err := oid4vci.VerifyJWTProof(testwallet.JWTProof(t, keys[0], issuer, "n2", ka), issuer, now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := keyattest.Verify(p.KeyAttestation, []x5c.Anchors{provider.Anchors("eudi")}, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Keys) != 2 || v.Nonce != "n2" || v.Anchor != "eudi" || v.KeyStorage[0] != "iso_18045_high" {
		t.Fatalf("unexpected key attestation %+v", v)
	}
	if _, err := keyattest.Verify(ka, []x5c.Anchors{testwallet.NewProvider(t, "other").Anchors("x")}, now, true); err == nil {
		t.Fatal("a key attestation from an untrusted provider must be refused")
	}
	noExp := provider.KeyAttestationClaims(t, keys, map[string]any{"nonce": "n3"})
	if _, err := keyattest.Verify(noExp, []x5c.Anchors{provider.Anchors("eudi")}, now, true); err == nil {
		t.Fatal("exp is required with a jwt proof")
	}
	if _, err := keyattest.Verify(noExp, []x5c.Anchors{provider.Anchors("eudi")}, now, false); err != nil {
		t.Fatalf("exp is optional with the attestation proof type: %v", err)
	}
	selfSigned := testwallet.Provider{Root: provider.Root, Signer: provider.Root}
	if _, err := keyattest.Verify(selfSigned.KeyAttestation(t, keys, "n4", now.Add(time.Hour)), []x5c.Anchors{provider.Anchors("eudi")}, now, true); err == nil {
		t.Fatal("a key attestation signed by the trust anchor itself must be refused")
	}
}
