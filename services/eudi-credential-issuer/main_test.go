package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
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

// Review finding 2: with only the nl profile, a set ISSUER_LOGIN_SERVER (the
// Compose default) turns the login off instead of stopping the issuer.
func TestConfigureLoginWithoutEUDIProfile(t *testing.T) {
	t.Setenv("ISSUER_LOGIN_SERVER", "http://localhost:9425")
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	cfg := issuance.Config{Profiles: []issuance.Profile{{Name: "nl", Kind: issuance.KindNL}}}
	if err := configureLogin(&cfg, log); err != nil {
		t.Fatalf("an nl-only configuration must start: %v", err)
	}
	if cfg.Login != nil {
		t.Error("without an eudi profile there is no login")
	}
	if !strings.Contains(out.String(), "login is off") {
		t.Errorf("the issuer must say why the login is off: %q", out.String())
	}
}
