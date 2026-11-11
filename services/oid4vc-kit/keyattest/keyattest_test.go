package keyattest_test

import (
	"crypto/ecdsa"
	"testing"
	"time"

	"gbo-demo/oid4vc-kit/keyattest"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

func TestVerify(t *testing.T) {
	provider := testwallet.NewProvider(t, "provider")
	anchors := []x5c.Anchors{provider.Anchors("p")}
	keys := []*ecdsa.PrivateKey{testpki.NewKey(t), testpki.NewKey(t)}
	now := time.Now()

	v, err := keyattest.Verify(provider.KeyAttestation(t, keys, "n", now.Add(time.Hour)), anchors, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Keys) != 2 || v.Nonce != "n" || v.Anchor != "p" ||
		len(v.KeyStorage) != 1 || v.KeyStorage[0] != "iso_18045_high" || v.UserAuthentication[0] != "iso_18045_high" {
		t.Fatalf("unexpected %+v", v)
	}
	for name, ka := range map[string]string{
		"expired":          provider.KeyAttestation(t, keys, "n", now.Add(-time.Second)),
		"no attested keys": provider.KeyAttestationClaims(t, nil, map[string]any{"exp": now.Add(time.Hour).Unix(), "attested_keys": []any{}}),
		"wrong typ":        testwallet.Sign(t, provider.Signer.Key, "jwt", nil, false, map[string]any{"iat": now.Unix()}),
	} {
		if _, err := keyattest.Verify(ka, anchors, now, true); err == nil {
			t.Errorf("%s: expected refusal", name)
		}
	}
}
