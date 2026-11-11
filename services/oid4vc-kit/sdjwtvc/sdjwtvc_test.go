package sdjwtvc_test

import (
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
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

// RFC 9901 section 7.1: a digest that occurs twice, and a disclosure whose
// claim name is not a string, are refused.
func TestParseRefusesMalformedDigests(t *testing.T) {
	root := testpki.NewRoot(t, "issuer root")
	issuer := root.Issue(t, testpki.Options{})
	anchors := []x5c.Anchors{x5c.NewAnchors("issuers", root.Cert)}
	disclose := func(name any) string {
		raw, _ := json.Marshal([]any{"salt", name, "v"})
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	sign := func(payload map[string]any) string {
		payload["_sd_alg"], payload["vct"] = "sha-256", "https://example.test/t"
		return testwallet.Sign(t, issuer.Key, sdjwtvc.Type, map[jose.HeaderKey]any{"x5c": x5c.Header(issuer.Chain)}, false, payload)
	}
	d := disclose("a")
	for name, tc := range map[string]struct{ combined, want string }{
		"a digest twice":         {sign(map[string]any{"_sd": []string{sdjwtvc.Digest(d), sdjwtvc.Digest(d)}}) + "~" + d + "~", "more than once"},
		"a digest in two places": {sign(map[string]any{"_sd": []string{sdjwtvc.Digest(d)}, "o": map[string]any{"_sd": []string{sdjwtvc.Digest(d)}}}) + "~" + d + "~", "more than once"},
		"a numeric claim name":   {sign(map[string]any{"_sd": []string{sdjwtvc.Digest(disclose(5))}}) + "~" + disclose(5) + "~", "must be a string"},
	} {
		if _, err := sdjwtvc.Parse(tc.combined, anchors, nil, time.Now()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// RFC 9901 section 4.1.1: without _sd_alg the digests are SHA-256; another
// or a non-string value is refused.
func TestParseSdAlg(t *testing.T) {
	root := testpki.NewRoot(t, "issuer root")
	issuer := root.Issue(t, testpki.Options{})
	anchors := []x5c.Anchors{x5c.NewAnchors("issuers", root.Cert)}
	raw, _ := json.Marshal([]any{"salt", "a", "v"})
	d := base64.RawURLEncoding.EncodeToString(raw)
	sign := func(alg any, set bool) string {
		payload := map[string]any{"vct": "https://example.test/t", "_sd": []string{sdjwtvc.Digest(d)}}
		if set {
			payload["_sd_alg"] = alg
		}
		return testwallet.Sign(t, issuer.Key, sdjwtvc.Type, map[jose.HeaderKey]any{"x5c": x5c.Header(issuer.Chain)}, false, payload) + "~" + d + "~"
	}
	parsed, err := sdjwtvc.Parse(sign(nil, false), anchors, nil, time.Now())
	if err != nil {
		t.Fatalf("without _sd_alg: %v", err)
	}
	if parsed.Claims["a"] != "v" {
		t.Errorf("without _sd_alg the disclosure is not in place: %v", parsed.Claims)
	}
	if _, ok := parsed.Claims["_sd_alg"]; ok {
		t.Error("_sd_alg must not be among the claims")
	}
	for name, alg := range map[string]any{"sha-512": "sha-512", "a number": 123, "an empty string": ""} {
		if _, err := sdjwtvc.Parse(sign(alg, true), anchors, nil, time.Now()); err == nil || !strings.Contains(err.Error(), "_sd_alg") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
