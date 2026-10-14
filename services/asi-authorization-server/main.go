// Command asi-authorization-server is the authorization server of the ASIP
// mock (ETSI TS 119 478 V1.1.1 clause 6.1.3, I4 Authorize). A QTSP obtains a
// DPoP-bound JWT access token here and presents it to services/asi-provider.
//
// The ASIP and the authorization server may be operated by one party, but
// they are separate roles (ASIP and AZSP), so they are separate services with
// separate keys.
//
// User identification is a stub: the user picks a test person on a page and
// approves or denies the request. It is not conformant identity proofing;
// slice 7 of #336 adds OpenID4VP PID disclosure.
//
// This file is the composition root: configuration, construction, lifecycle.
//
// Usage:
//
//	asi-authorization-server                              serve (configuration from the environment)
//	asi-authorization-server provision-qtsp-certificate   issue a test QTSP certificate
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/clientconfig"
	"gbo-demo/asi-authorization-server/internal/httpapi"
	"gbo-demo/asi-authorization-server/internal/qtspcert"
	"gbo-demo/asi-authorization-server/internal/stubregister"
	"gbo-demo/asi-authorization-server/internal/testpersons"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "asi-authorization-server"))
	if len(os.Args) > 1 && os.Args[1] == "provision-qtsp-certificate" {
		if err := qtspcert.Provision(os.Args[2:]); err != nil {
			slog.Error("provisioning QTSP certificate", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := serve(); err != nil {
		slog.Error("serving asi-authorization-server", "err", err)
		os.Exit(1)
	}
}

func serve() error {
	issuer := strings.TrimRight(env("AZSP_ISSUER", "http://localhost:4021"), "/")
	key, err := loadSigningKey(os.Getenv("AZSP_SIGNING_KEY_FILE"))
	if err != nil {
		return err
	}
	tokens, err := authorization.NewTokenIssuer(issuer, key)
	if err != nil {
		return err
	}
	clients, err := clientconfig.Load(os.Getenv("AZSP_CLIENTS_FILE"))
	if err != nil {
		return err
	}
	persons, err := testpersons.Load(env("AZSP_TEST_PERSONS_FILE", "testdata/test-persons.json"))
	if err != nil {
		return err
	}
	register, err := stubregister.Load(env("AZSP_REGISTER_FILE", "testdata/register.json"))
	if err != nil {
		return err
	}
	anchors, err := loadTrustAnchors(os.Getenv("AZSP_TRUST_ANCHORS_FILE"))
	if err != nil {
		return err
	}
	recheckInterval, err := time.ParseDuration(env("AZSP_ENTITLEMENT_RECHECK_INTERVAL", "1h"))
	if err != nil || recheckInterval <= 0 {
		return fmt.Errorf("AZSP_ENTITLEMENT_RECHECK_INTERVAL: %q is not a positive duration", os.Getenv("AZSP_ENTITLEMENT_RECHECK_INTERVAL"))
	}
	// The base URLs of the ASIP's data services, separated by commas.
	var resources []string
	for _, r := range strings.Split(env("AZSP_RESOURCES", "http://localhost:4020/brp/authsrc-api"), ",") {
		if r = strings.TrimRight(strings.TrimSpace(r), "/"); r != "" {
			resources = append(resources, r)
		}
	}
	if len(resources) == 0 {
		return errors.New("AZSP_RESOURCES names no data service of the ASIP")
	}
	service := authorization.NewService(authorization.Config{
		Issuer:       issuer,
		Resources:    resources,
		Clients:      clients,
		Identifier:   persons,
		Tokens:       tokens,
		Register:     register,
		TrustAnchors: anchors,
	})
	srv := &http.Server{Addr: env("AZSP_ADDR", ":4021"), Handler: httpapi.NewHandler(service), ReadHeaderTimeout: readHeaderTimeout}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "issuer", issuer, "clients", clients.Len(), "register_entries", register.Len(),
			"test_persons", persons.Len(), "identification", "stub: test-person chooser, not conformant")
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go recheckEntitlements(ctx, service, recheckInterval)
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

// recheckEntitlements verifies the entitlements of the registered clients
// again at every interval (ETSI TS 119 478 clause 6.1.3.2, figure 2, step 2).
func recheckEntitlements(ctx context.Context, service *authorization.Service, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res := service.RecheckEntitlements(ctx)
			slog.Info("entitlements re-verified", "checked", res.Checked, "deactivated", res.Deactivated, "register_unavailable", res.Unavailable)
		}
	}
}

// loadTrustAnchors reads the roots the certificates of registering clients
// must chain to: the development reader CA. Without them, dynamic client
// registration refuses every client.
func loadTrustAnchors(path string) (*x509.CertPool, error) {
	if path == "" {
		slog.Warn("AZSP_TRUST_ANCHORS_FILE not set: dynamic client registration refuses every client")
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read trust anchors: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, errors.New("trust anchors file contains no certificate")
	}
	return pool, nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// loadSigningKey reads the ES256 token signing key. Without a file the server
// generates a key at start: tokens then do not survive a restart, which is
// acceptable for tokens that live ten minutes.
func loadSigningKey(path string) (*ecdsa.PrivateKey, error) {
	if path == "" {
		slog.Warn("AZSP_SIGNING_KEY_FILE not set: using an ephemeral signing key")
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("signing key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return x509.ParseECPrivateKey(block.Bytes)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("signing key must be an ECDSA P-256 key")
	}
	return key, nil
}
