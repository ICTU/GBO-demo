package sdjwtvc_test

import (
	"encoding/asn1"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/x5c"
)

var mdlIssuerEKU = asn1.ObjectIdentifier{1, 0, 18013, 5, 1, 2}

func TestIssueAndParseRoundTrip(t *testing.T) {
	root := testpki.NewRoot(t, "issuer root")
	issuer := root.Issue(t, testpki.Options{EKU: []asn1.ObjectIdentifier{mdlIssuerEKU}})
	signer, err := sdjwtvc.NewSigner(issuer.Key, x5c.Header(issuer.Chain))
	if err != nil {
		t.Fatal(err)
	}
	holder := testpki.NewKey(t)
	now := time.Now().Truncate(time.Second)
	cred, err := signer.Issue(sdjwtvc.Credential{
		VCT:       "https://example.test/types/income",
		IssuedAt:  now,
		NotBefore: now,
		Expires:   now.Add(24 * time.Hour),
		HolderKey: &jose.JSONWebKey{Key: &holder.PublicKey},
		Status:    &sdjwtvc.StatusReference{Index: 7, URI: "https://example.test/status/a"},
		Claims: map[string]any{
			"year":  "2025",
			"box1":  map[string]any{"amount": 1200.5, "currency": "EUR"},
			"lines": []any{"a", "b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cred, "~") || strings.Count(cred, "~") != 6 {
		t.Fatalf("want 5 disclosures in combined format, got %q", cred)
	}

	parsed, err := sdjwtvc.Parse(cred, []x5c.Anchors{x5c.NewAnchors("issuers", root.Cert)}, mdlIssuerEKU, now)
	if err != nil {
		t.Fatal(err)
	}
	c := parsed.Claims
	if c["year"] != "2025" || c["vct"] != "https://example.test/types/income" {
		t.Fatalf("claims not restored: %v", c)
	}
	box, _ := c["box1"].(map[string]any)
	if box["amount"] != 1200.5 || box["currency"] != "EUR" {
		t.Fatalf("nested claims not restored: %v", box)
	}
	if _, ok := c["iss"]; ok {
		t.Fatal("the credential must not carry iss")
	}
	status, _ := c["status"].(map[string]any)
	if ref, _ := status["status_list"].(map[string]any); ref["idx"] != float64(7) {
		t.Fatalf("status reference missing: %v", c["status"])
	}
}

func TestParseRejectsWrongEKUAndTamperedDisclosure(t *testing.T) {
	root := testpki.NewRoot(t, "issuer root")
	issuer := root.Issue(t, testpki.Options{})
	signer, _ := sdjwtvc.NewSigner(issuer.Key, x5c.Header(issuer.Chain))
	holder := testpki.NewKey(t)
	now := time.Now()
	cred, err := signer.Issue(sdjwtvc.Credential{VCT: "x", IssuedAt: now, NotBefore: now, Expires: now.Add(time.Hour),
		HolderKey: &jose.JSONWebKey{Key: &holder.PublicKey}, Claims: map[string]any{"a": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	anchors := []x5c.Anchors{x5c.NewAnchors("issuers", root.Cert)}
	if _, err := sdjwtvc.Parse(cred, anchors, mdlIssuerEKU, now); err == nil {
		t.Fatal("a leaf without the required EKU must be refused")
	}
	parts := strings.Split(cred, "~")
	parts[1] = "WyJzYWx0IiwiYSIsImMiXQ" // ["salt","a","c"]
	if _, err := sdjwtvc.Parse(strings.Join(parts, "~"), anchors, nil, now); err == nil {
		t.Fatal("a disclosure without a matching digest must be refused")
	}
}

func TestReservedClaimsAreNotConcealed(t *testing.T) {
	root := testpki.NewRoot(t, "issuer root")
	issuer := root.Issue(t, testpki.Options{})
	signer, _ := sdjwtvc.NewSigner(issuer.Key, x5c.Header(issuer.Chain))
	holder := testpki.NewKey(t)
	_, err := signer.Issue(sdjwtvc.Credential{VCT: "x", HolderKey: &jose.JSONWebKey{Key: &holder.PublicKey},
		Claims: map[string]any{"exp": 1}})
	if err == nil {
		t.Fatal("a reserved claim among the attributes must be refused")
	}
	// Reserved names are reserved at the top level only.
	if _, err := signer.Issue(sdjwtvc.Credential{VCT: "x", HolderKey: &jose.JSONWebKey{Key: &holder.PublicKey},
		Claims: map[string]any{"employment": map[string]any{"status": "employed"}}}); err != nil {
		t.Fatalf("a nested claim named status must be allowed: %v", err)
	}
}
