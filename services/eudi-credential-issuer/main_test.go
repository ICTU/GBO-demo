package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The PID status fetcher fetches https only and follows no redirect.
func TestPIDStatusFetcher(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/list", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("token"))
	}))
	defer srv.Close()
	fetch := pidStatusFetcher(srv.Client())
	ctx := context.Background()
	if token, err := fetch(ctx, srv.URL+"/list"); err != nil || token != "token" {
		t.Fatalf("%q %v", token, err)
	}
	if _, err := fetch(ctx, srv.URL+"/redirect"); err == nil {
		t.Error("a redirect must not be followed")
	}
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("token")) }))
	defer plain.Close()
	if _, err := fetch(ctx, plain.URL+"/list"); err == nil {
		t.Error("http must be refused")
	}
}
