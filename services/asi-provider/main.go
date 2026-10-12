// Command asi-provider is a mock Authentic Source Interface Provider (ASIP)
// after ETSI TS 119 478 V1.1.1 clause 6.1: a QTSP verifies whether an
// attribute it holds matches the authentic source.
//
// /verify and /retrieve answer against an in-memory source, and every result
// is sealed. Every request carries a DPoP-bound access token from the
// authorization server (services/asi-authorization-server, clause 6.1.3);
// the user's BSN comes from that token.
//
// This file is the composition root: configuration, construction, lifecycle.
//
// Usage:
//
//	asi-provider                              serve (configuration from the environment)
//	asi-provider provision-seal-certificate   issue a development seal certificate
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gbo-demo/asi-provider/internal/accesstoken"
	"gbo-demo/asi-provider/internal/asmetadata"
	"gbo-demo/asi-provider/internal/httpapi"
	"gbo-demo/asi-provider/internal/memorysource"
	"gbo-demo/asi-provider/internal/seal"
	"gbo-demo/asi-provider/internal/sealcert"
	"gbo-demo/asi-provider/internal/verification"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "asi-provider"))
	if len(os.Args) > 1 && os.Args[1] == "provision-seal-certificate" {
		if err := sealcert.Provision(os.Args[2:]); err != nil {
			slog.Error("provisioning seal certificate", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := serve(); err != nil {
		slog.Error("serving asi-provider", "err", err)
		os.Exit(1)
	}
}

func serve() error {
	source, err := memorysource.Load(env("ASIP_SOURCE_FILE", "testdata/persons.json"))
	if err != nil {
		return err
	}
	key, chain, err := seal.LoadMaterial(os.Getenv("ASIP_SEAL_KEY_FILE"), os.Getenv("ASIP_SEAL_CERT_FILE"))
	if err != nil {
		return err
	}
	issuer := strings.TrimRight(env("ASIP_AUTHORIZATION_SERVER", "http://localhost:4021"), "/")
	publicURL := strings.TrimRight(env("ASIP_PUBLIC_URL", "http://localhost:4020"), "/")
	resource := publicURL + httpapi.BasePath
	handler, err := httpapi.NewHandler(httpapi.Config{
		Verification:    verification.NewService(source, verification.DefaultCatalogue()),
		Tokens:          accesstoken.NewValidator(issuer, resource, publicURL, asmetadata.New(issuer)),
		Sealer:          seal.New(key, chain),
		Provider:        httpapi.Provider{LegalName: env("ASIP_PROVIDER_NAME", "GBO demo ASIP")},
		AuthenticSource: httpapi.Provider{LegalName: env("ASIP_AUTHENTIC_SOURCE_NAME", "Basisregistratie Personen (mock)")},
	})
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: env("ASIP_ADDR", ":4020"), Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "resource", resource, "authorization_server", issuer)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		slog.Warn("graceful shutdown did not finish", "err", err)
	}
	return nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
