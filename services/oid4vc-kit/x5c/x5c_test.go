package x5c_test

import (
	"encoding/asn1"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/x5c"
)

var eku = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 127}

// header signs a JWT with c and returns its header with c's chain in x5c.
func header(t *testing.T, c testpki.Cert) jose.Header {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: c.Key},
		(&jose.SignerOptions{}).WithHeader("x5c", x5c.Header(c.Chain)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(map[string]any{"a": 1}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	return tok.Headers[0]
}

func TestVerify(t *testing.T) {
	root := testpki.NewRoot(t, "root")
	ca := root.IssueCA(t, "intermediate")
	leaf := ca.Issue(t, testpki.Options{EKU: []asn1.ObjectIdentifier{eku}})
	other := testpki.NewRoot(t, "other")
	now := time.Now()

	got, name, err := x5c.Verify(header(t, leaf), []x5c.Anchors{x5c.NewAnchors("other", other.Cert), x5c.NewAnchors("ours", root.Cert)}, now, eku)
	if err != nil || name != "ours" || !got.Equal(leaf.Cert) {
		t.Fatalf("got %v %q %v", got, name, err)
	}
	if _, _, err := x5c.Verify(header(t, leaf), []x5c.Anchors{x5c.NewAnchors("other", other.Cert)}, now, nil); err == nil {
		t.Fatal("a chain to an unknown root must be refused")
	}
	plain := ca.Issue(t, testpki.Options{})
	if _, _, err := x5c.Verify(header(t, plain), []x5c.Anchors{x5c.NewAnchors("ours", root.Cert)}, now, eku); err == nil {
		t.Fatal("a leaf without the required EKU must be refused")
	}
	// HAIP: the signing certificate must not be self-signed, also when it
	// is trusted as its own anchor and is not a CA.
	self := testpki.NewSelfSignedLeaf(t, "self")
	if _, _, err := x5c.Verify(header(t, self), []x5c.Anchors{x5c.NewAnchors("self", self.Cert)}, now, nil); err == nil {
		t.Fatal("a self-signed signing certificate must be refused")
	}
}

func TestSelfSigned(t *testing.T) {
	root := testpki.NewRoot(t, "root")
	for name, c := range map[string]struct {
		cert testpki.Cert
		want bool
	}{
		"CA root":          {root, true},
		"non-CA self-sign": {testpki.NewSelfSignedLeaf(t, "leaf"), true},
		"issued leaf":      {root.Issue(t, testpki.Options{}), false},
		"intermediate":     {root.IssueCA(t, "ca"), false},
	} {
		if got := x5c.SelfSigned(c.cert.Cert); got != c.want {
			t.Errorf("%s: SelfSigned = %v, want %v", name, got, c.want)
		}
	}
}
