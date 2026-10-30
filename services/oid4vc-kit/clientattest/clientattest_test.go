package clientattest_test

import (
	"errors"
	"testing"
	"time"

	"gbo-demo/oid4vc-kit/clientattest"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
)

const aud = "https://issuer.example.test/nl"

func TestVerify(t *testing.T) {
	provider := testwallet.NewProvider(t, "nl provider")
	other := testwallet.NewProvider(t, "other provider")
	anchors := []x5c.Anchors{provider.Anchors("nl")}
	key := testpki.NewKey(t)
	now := time.Now()
	wia := provider.WIA(t, "https://wallet.example.test", key, now.Add(time.Hour))

	v, err := clientattest.Verify(wia, testwallet.PoP(t, key, "https://wallet.example.test", aud, "c1"), anchors, aud, now)
	if err != nil {
		t.Fatal(err)
	}
	if v.ClientID != "https://wallet.example.test" || v.Anchor != "nl" || v.Challenge != "c1" || v.PoPID == "" {
		t.Fatalf("unexpected result %+v", v)
	}

	cases := map[string]struct {
		wia, pop string
		expired  bool
	}{
		"unknown wallet provider": {wia: other.WIA(t, "w", key, now.Add(time.Hour)), pop: testwallet.PoP(t, key, "w", aud, "")},
		"expired attestation":     {wia: provider.WIA(t, "w", key, now.Add(-time.Second)), pop: testwallet.PoP(t, key, "w", aud, ""), expired: true},
		"PoP for another server":  {wia: wia, pop: testwallet.PoP(t, key, "https://wallet.example.test", "https://elsewhere.test", "")},
		"PoP by another key":      {wia: wia, pop: testwallet.PoP(t, testpki.NewKey(t), "https://wallet.example.test", aud, "")},
		"PoP from another client": {wia: wia, pop: testwallet.PoP(t, key, "someone-else", aud, "")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := clientattest.Verify(c.wia, c.pop, anchors, aud, now)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if errors.Is(err, clientattest.ErrExpired) != c.expired {
				t.Fatalf("expired = %v, want %v (%v)", errors.Is(err, clientattest.ErrExpired), c.expired, err)
			}
		})
	}
}
