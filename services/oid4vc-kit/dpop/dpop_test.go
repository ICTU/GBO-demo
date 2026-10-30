package dpop_test

import (
	"testing"
	"time"

	"gbo-demo/oid4vc-kit/dpop"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
)

func TestVerify(t *testing.T) {
	key := testpki.NewKey(t)
	url := "https://issuer.example.test/nl/credential"
	now := time.Now()
	proof := testwallet.DPoP(t, key, "POST", url, "at-1")

	p, err := dpop.Verify([]string{proof}, dpop.Expected{Method: "POST", URL: url + "?x=1", AccessToken: "at-1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if p.JKT == "" || p.JTI == "" {
		t.Fatalf("unexpected proof %+v", p)
	}
	for name, c := range map[string]struct {
		headers []string
		want    dpop.Expected
		now     time.Time
	}{
		"no proof":           {nil, dpop.Expected{Method: "POST", URL: url}, now},
		"other URL":          {[]string{proof}, dpop.Expected{Method: "POST", URL: "https://issuer.example.test/nl/token"}, now},
		"other access token": {[]string{proof}, dpop.Expected{Method: "POST", URL: url, AccessToken: "at-2"}, now},
		"too old":            {[]string{proof}, dpop.Expected{Method: "POST", URL: url}, now.Add(2 * time.Minute)},
		"two proofs":         {[]string{proof, proof}, dpop.Expected{Method: "POST", URL: url}, now},
		"other method":       {[]string{proof}, dpop.Expected{Method: "GET", URL: url}, now},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := dpop.Verify(c.headers, c.want, c.now); err == nil {
				t.Fatal("expected refusal")
			}
		})
	}
}
