package lote_test

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gbo-demo/oid4vc-kit/lote"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/testwallet"
	"gbo-demo/oid4vc-kit/x5c"
	"github.com/go-jose/go-jose/v4"
)

const prefix = "http://uri.etsi.org/19602/"

func signed(t *testing.T, signer testpki.Cert, role string, seq int, issue, next time.Time, certs ...*x509.Certificate) string {
	t.Helper()
	var entities []any
	for _, cert := range certs {
		entities = append(entities, map[string]any{"TrustedEntityServices": []any{map[string]any{"ServiceInformation": map[string]any{
			"ServiceTypeIdentifier":  prefix + "SvcType/" + role + "/Issuance",
			"ServiceDigitalIdentity": map[string]any{"X509Certificates": []any{map[string]any{"val": base64.StdEncoding.EncodeToString(cert.Raw)}}},
		}}}})
	}
	return testwallet.Sign(t, signer.Key, "JOSE+JSON", map[jose.HeaderKey]any{"x5c": x5c.Header(signer.Chain)}, false, map[string]any{"LoTE": map[string]any{
		"ListAndSchemeInformation": map[string]any{"LoTEVersionIdentifier": 1, "LoTEType": prefix + "LoTEType/EU" + role + "ProvidersList", "LoTESequenceNumber": seq, "ListIssueDateTime": issue, "NextUpdate": next}, "TrustedEntitiesList": entities}})
}

// I1, I2: actual JWS, PKIX and TLS checks, independent of the publisher.
func TestAuthenticationAndRole(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := testpki.NewRoot(t, "LoTE root")
	signer := root.Issue(t, testpki.Options{})
	pid := testpki.NewRoot(t, "NL PID")
	other := testpki.NewRoot(t, "unknown signer").Issue(t, testpki.Options{})
	withUsage := func(usage x509.KeyUsage) *x509.Certificate {
		template := *pid.Cert
		template.KeyUsage = usage
		der, err := x509.CreateCertificate(rand.Reader, &template, &template, pid.Cert.PublicKey, pid.Key)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return cert
	}
	noUsage := withUsage(0)
	forbiddenUsage := withUsage(x509.KeyUsageDigitalSignature)
	valid := signed(t, signer, "PID", 1, now.Add(-time.Minute), now.Add(2*time.Hour), pid.Cert)
	parts := strings.Split(valid, ".")
	parts[2] = "AAAA"
	tampered := strings.Join(parts, ".")
	for _, tc := range []struct {
		name, raw string
		ok        bool
	}{
		{"valid", valid, true},
		{"CA without optional key usage", signed(t, signer, "PID", 1, now.Add(-time.Minute), now.Add(time.Hour), noUsage), true},
		{"CA with key usage forbidding signing", signed(t, signer, "PID", 1, now.Add(-time.Minute), now.Add(time.Hour), forbiddenUsage), false},
		{"unknown signer", signed(t, other, "PID", 1, now.Add(-time.Minute), now.Add(time.Hour), pid.Cert), false},
		{"wrong role", signed(t, signer, "PubEAA", 1, now.Add(-time.Minute), now.Add(time.Hour), pid.Cert), false},
		{"expired", signed(t, signer, "PID", 1, now.Add(-time.Hour), now, pid.Cert), false},
		{"future", signed(t, signer, "PID", 1, now.Add(time.Minute), now.Add(time.Hour), pid.Cert), false},
		{"invalid signature", tampered, false},
		{"leaf as CA", signed(t, signer, "PID", 1, now.Add(-time.Minute), now.Add(time.Hour), other.Cert), false},
		{"no issuance roots", signed(t, signer, "PID", 1, now.Add(-time.Minute), now.Add(time.Hour)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(tc.raw)) }))
			defer srv.Close()
			c, err := lote.New(srv.URL, "PID", []*x509.Certificate{root.Cert}, srv.Client(), func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			trust, err := c.Load(context.Background())
			if (err == nil) != tc.ok {
				t.Fatalf("accepted=%v, want %v: %v", err == nil, tc.ok, err)
			}
			if tc.ok && (len(trust.Anchors) != 1 || len(trust.Authorities) != 1) {
				t.Fatal("missing roots/discovery hints")
			}
		})
	}
}

// I3-I5: in-use hourly refresh, removed roots, errors and expiry. A bad
// refresh never extends the previously authenticated validity interval.
func TestRefreshAndExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := testpki.NewRoot(t, "LoTE root")
	signer := root.Issue(t, testpki.Options{})
	old := testpki.NewRoot(t, "old PID")
	next := testpki.NewRoot(t, "new PID")
	leaf := old.Issue(t, testpki.Options{})
	var current atomic.Value
	current.Store(signed(t, signer, "PID", 1, now.Add(-time.Minute), now.Add(3*time.Hour), old.Cert))
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw := current.Load().(string)
		if raw == "" {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(raw))
	}))
	defer srv.Close()
	c, err := lote.New(srv.URL, "PID", []*x509.Certificate{root.Cert}, srv.Client(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	check := func(oldOK bool) {
		t.Helper()
		trust, err := c.Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		header := jose.Header{ExtraHeaders: map[jose.HeaderKey]any{"x5c": x5c.Header(leaf.Chain)}}
		// Parse a real JWS to let jose decode its certificate header.
		raw := testwallet.Sign(t, leaf.Key, "test", header.ExtraHeaders, false, map[string]any{})
		j, _ := jose.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
		_, _, err = x5c.Verify(j.Signatures[0].Header, trust.Anchors, now, nil)
		if (err == nil) != oldOK {
			t.Fatalf("old issuer accepted=%v, want %v", err == nil, oldOK)
		}
	}
	check(true)
	current.Store(signed(t, signer, "PID", 2, now.Add(-time.Minute), now.Add(3*time.Hour), next.Cert))
	now = now.Add(59 * time.Minute)
	check(true)
	now = now.Add(time.Minute)
	check(false)
	if calls.Load() != 2 {
		t.Fatalf("requests=%d", calls.Load())
	}
	current.Store("")
	now = now.Add(time.Hour)
	check(false) // last good cache
	now = now.Add(time.Hour)
	if _, err := c.Load(context.Background()); err == nil {
		t.Fatal("expired cache accepted")
	}
	current.Store(signed(t, signer, "PID", 3, now.Add(-time.Minute), now.Add(time.Hour), next.Cert))
	now = now.Add(time.Minute)
	check(false)
}

func TestSequenceRollbackAndConcurrentRefresh(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := testpki.NewRoot(t, "LoTE root")
	signer := root.Issue(t, testpki.Options{})
	pid := testpki.NewRoot(t, "PID")
	other := testpki.NewRoot(t, "other PID")
	var raw atomic.Value
	raw.Store(signed(t, signer, "PID", 2, now.Add(-time.Minute), now.Add(4*time.Hour), pid.Cert))
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(raw.Load().(string)))
	}))
	defer srv.Close()
	c, _ := lote.New(srv.URL, "PID", []*x509.Certificate{root.Cert}, srv.Client(), func() time.Time { return now })
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Load(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("parallel fetches: %d", calls.Load())
	}
	original, _ := c.Load(context.Background())
	for _, seq := range []int{1, 2} {
		now = now.Add(time.Hour)
		raw.Store(signed(t, signer, "PID", seq, now.Add(-time.Minute), now.Add(3*time.Hour), other.Cert))
		got, err := c.Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.Authorities[0] != original.Authorities[0] {
			t.Fatal("rollback/conflicting sequence replaced cache")
		}
	}
}

func TestHTTPSOnlyAndNoRedirect(t *testing.T) {
	root := testpki.NewRoot(t, "LoTE root")
	if _, err := lote.New("http://example.test/PID.jwt", "PID", []*x509.Certificate{root.Cert}, nil, nil); err == nil {
		t.Fatal("HTTP accepted")
	}
	var redirected atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/other" {
			redirected.Add(1)
		}
		http.Redirect(w, r, "/other", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := lote.New(srv.URL, "PID", []*x509.Certificate{root.Cert}, srv.Client(), nil)
	if _, err := c.Load(context.Background()); err == nil || redirected.Load() != 0 {
		t.Fatal("redirect was followed or accepted")
	}
	c, _ = lote.New(srv.URL, "PID", []*x509.Certificate{root.Cert}, nil, nil)
	if _, err := c.Load(context.Background()); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
}
