// Command asi-provider is a mock Authentic Source Interface Provider (ASIP)
// after ETSI TS 119 478 V1.1.1 clause 6.1: a QTSP verifies whether an
// attribute it holds matches the authentic source.
//
// The ASIP serves a data service per authentic source, configured in
// ASIP_DATASERVICES_FILE, under /<data service>/authsrc-api. /verify and
// /retrieve answer against that source, asked over FSC through the ASIP's own
// Outway, and every result is sealed. Every request carries a DPoP-bound access token from the
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
	"gbo-demo/asi-provider/internal/brpsource"
	"gbo-demo/asi-provider/internal/dataservice"
	"gbo-demo/asi-provider/internal/httpapi"
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
	outway := os.Getenv("ASIP_OUTWAY_URL")
	if outway == "" {
		return errors.New("ASIP_OUTWAY_URL is required: the ASIP's FSC Outway, through which it asks the authentic sources")
	}
	services, err := dataservice.Load(env("ASIP_DATASERVICES_FILE", "config/dataservices.json"), adapterAttributes)
	if err != nil {
		return err
	}
	key, chain, err := seal.LoadMaterial(os.Getenv("ASIP_SEAL_KEY_FILE"), os.Getenv("ASIP_SEAL_CERT_FILE"))
	if err != nil {
		return err
	}
	issuer := strings.TrimRight(env("ASIP_AUTHORIZATION_SERVER", "http://localhost:4021"), "/")
	publicURL := strings.TrimRight(env("ASIP_PUBLIC_URL", "http://localhost:4020"), "/")
	// The address the ASIP reaches the authorization server on, when it
	// differs from the issuer (compose: the issuer is a host address).
	issuerInternal := os.Getenv("ASIP_AUTHORIZATION_SERVER_INTERNAL_URL")
	keys := asmetadata.New(issuer, issuerInternal)
	var dataServices []httpapi.DataService
	for _, ds := range services {
		catalogue := verification.Catalogue{}
		for id, name := range ds.Attributes {
			catalogue[id] = verification.Entry{Name: name}
		}
		var authenticSource *httpapi.Provider
		if ds.AuthenticSource != "" {
			authenticSource = &httpapi.Provider{LegalName: ds.AuthenticSource}
		}
		dataServices = append(dataServices, httpapi.DataService{
			ID:              ds.ID,
			Verification:    verification.NewService(newAdapter(ds.Source, outway, ds.GrantPath), catalogue),
			Tokens:          accesstoken.NewValidator(issuer, publicURL+httpapi.BasePathOf(ds.ID), publicURL, keys),
			Provider:        httpapi.Provider{LegalName: ds.Provider},
			AuthenticSource: authenticSource,
		})
	}
	handler, err := httpapi.NewHandler(httpapi.Config{PublicURL: publicURL, Issuer: issuer, Sealer: seal.New(key, chain), DataServices: dataServices})
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: env("ASIP_ADDR", ":4020"), Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "public_url", publicURL, "data_services", len(dataServices), "authorization_server", issuer, "outway", outway)
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

// adapterAttributes lists per source adapter the attributes it can ask for;
// the data services file may name only these.
var adapterAttributes = map[string][]string{"brp": brpsource.Attributes()}

// newAdapter returns the adapter that asks an authentic source through the
// Outway. dataservice.Load has checked the name.
func newAdapter(name, outway, grantPath string) verification.Source {
	switch name {
	case "brp":
		return brpsource.New(outway, grantPath)
	}
	panic("no adapter " + name)
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
