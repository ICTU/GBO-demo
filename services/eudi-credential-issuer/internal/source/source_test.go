package source_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/source"
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
