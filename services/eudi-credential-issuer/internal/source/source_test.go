package source_test

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/source"
	"gbo-demo/oid4vc-kit/testpki"
)

func TestFetch(t *testing.T) {
	var gotBody map[string]any
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		gotQuery = r.URL.Query().Get("jaar")
		_, _ = io.WriteString(w, `[{"id":"1","format":"dc+sd-jwt","attestation_type":"t","attributes":{"big":9007199254740993}}]`)
	}))
	defer srv.Close()
	c := &source.Client{HTTP: srv.Client(), Endpoints: map[string]string{"income": srv.URL + "/a?x=1"}}

	docs, err := c.Fetch(context.Background(), "income", map[string]any{"bsn": "1"}, map[string]string{"jaar": "2025"})
	if err != nil {
		t.Fatal(err)
	}
	// The request is {"subject": ...}, not a disclosure array: a backend
	// can tell the offer caller named the subject.
	subject, ok := gotBody["subject"].(map[string]any)
	if !ok || subject["bsn"] != "1" || gotQuery != "2025" {
		t.Fatalf("body %v, query %q", gotBody, gotQuery)
	}
	if got := docs[0].Attributes["big"].(json.Number).String(); got != "9007199254740993" {
		t.Fatalf("whole numbers must keep their value, got %s", got)
	}
	if _, err := c.Fetch(context.Background(), "unknown", nil, nil); !errors.Is(err, issuance.ErrSourceUnavailable) {
		t.Fatalf("a configuration without endpoint: %v", err)
	}
}

func TestFetchStatuses(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusNotFound:   issuance.ErrNoData,
		http.StatusBadRequest: issuance.ErrSourceUnavailable,
		http.StatusBadGateway: issuance.ErrSourceUnavailable,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		c := &source.Client{HTTP: srv.Client(), Endpoints: map[string]string{"income": srv.URL}}
		if _, err := c.Fetch(context.Background(), "income", map[string]any{"bsn": "1"}, nil); !errors.Is(err, want) {
			t.Errorf("status %d: got %v, want %v", status, err, want)
		}
		srv.Close()
	}
}

// P3: a bronpoort with a TLS certificate of its own is reached with its
// roots, and refused with others.
func TestFetchWithOwnRoots(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"1","format":"dc+sd-jwt","attestation_type":"t","attributes":{"a":"b"}}]`)
	}))
	defer srv.Close()
	own := x509.NewCertPool()
	own.AddCert(srv.Certificate())
	foreign := x509.NewCertPool()
	foreign.AddCert(testpki.NewRoot(t, "another CA").Cert)

	base := &http.Client{}
	c := &source.Client{HTTP: base, Endpoints: map[string]string{"qtsp": srv.URL, "plain": srv.URL},
		Clients: map[string]*http.Client{"qtsp": source.WithRoots(base, own)}}
	if docs, err := c.Fetch(context.Background(), "qtsp", map[string]any{"x": "1"}, nil); err != nil || len(docs) != 1 {
		t.Fatalf("own roots: %v %v", docs, err)
	}
	if _, err := c.Fetch(context.Background(), "plain", map[string]any{"x": "1"}, nil); !errors.Is(err, issuance.ErrSourceUnavailable) {
		t.Fatalf("without the roots: %v", err)
	}
	c.Clients["qtsp"] = source.WithRoots(base, foreign)
	if _, err := c.Fetch(context.Background(), "qtsp", map[string]any{"x": "1"}, nil); !errors.Is(err, issuance.ErrSourceUnavailable) {
		t.Fatalf("other roots: %v", err)
	}
}

// A secret in the bronpoort URL (the QTSP callback path) stays out of the
// error, which may be logged or answered.
func TestFetchErrorHidesURL(t *testing.T) {
	c := &source.Client{HTTP: &http.Client{}, Endpoints: map[string]string{"qtsp": "https://127.0.0.1:1/attestations/s3cret-token"}}
	_, err := c.Fetch(context.Background(), "qtsp", map[string]any{"x": "1"}, nil)
	if !errors.Is(err, issuance.ErrSourceUnavailable) || strings.Contains(err.Error(), "s3cret-token") {
		t.Fatalf("%v", err)
	}
}
