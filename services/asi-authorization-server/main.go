// Command asi-authorization-server is the authorization server of the ASIP
// mock (ETSI TS 119 478 V1.1.1 clause 6.1.3, I4 Authorize). A QTSP obtains a
// DPoP-bound JWT access token here and presents it to services/asi-provider.
//
// The ASIP and the authorization server may be operated by one party, but
// they are separate roles (ASIP and AZSP), so they are separate services with
// separate keys.
//
// User identification: one page shows the request, and the user logs in or
// denies. Two means: a DigiD mock, which provides the BSN only and is not
// conformant identity proofing, and the NL Wallet through OpenID4VP PID
// disclosure, offered when a verification server is configured.
//
// This file is the composition root: configuration, construction, lifecycle.
//
// Usage:
//
//	asi-authorization-server                              serve (configuration from the environment)
//	asi-authorization-server provision-qtsp-certificate   issue a test QTSP certificate
//	asi-authorization-server provision-wallet-verifier    configure the NL Wallet verifier
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
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"gbo-demo/asi-authorization-server/internal/authorization"
	"gbo-demo/asi-authorization-server/internal/clientconfig"
	"gbo-demo/asi-authorization-server/internal/digidmock"
	"gbo-demo/asi-authorization-server/internal/httpapi"
	"gbo-demo/asi-authorization-server/internal/qtspcert"
	"gbo-demo/asi-authorization-server/internal/stubregister"
	"gbo-demo/asi-authorization-server/internal/walletverifier"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "asi-authorization-server"))
	if len(os.Args) > 1 && os.Args[1] == "provision-wallet-verifier" {
		if err := walletverifier.Provision(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
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
	resources, err := parseResources(env("AZSP_RESOURCES", "http://localhost:4020/brp/authsrc-api=Basisregistratie Personen (mock)"))
	if err != nil {
		return err
	}
	identifiers := map[string]authorization.Identifier{digidmock.Means: digidmock.Identifier{}}
	if wallet, err := walletVerifier(); err != nil {
		return err
	} else if wallet != nil {
		identifiers[walletverifier.Means] = wallet
	}
	service := authorization.NewService(authorization.Config{
		Issuer:       issuer,
		Resources:    resources,
		Clients:      clients,
		Identifiers:  identifiers,
		Tokens:       tokens,
		Register:     register,
		TrustAnchors: anchors,
	})
	srv := &http.Server{Addr: env("AZSP_ADDR", ":4021"), Handler: httpapi.NewHandler(service), ReadHeaderTimeout: readHeaderTimeout}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "issuer", issuer, "clients", clients.Len(), "register_entries", register.Len(),
			"identification_means", slices.Sorted(maps.Keys(identifiers)))
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

// parseResources reads the ASIP's data services: base URL and the name of
// its authentic source, as url=name, separated by commas. The user sees the
// name when approving, so it is required.
func parseResources(value string) ([]authorization.Resource, error) {
	var resources []authorization.Resource
	for _, entry := range strings.Split(value, ",") {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		url, name, _ := strings.Cut(entry, "=")
		url, name = strings.TrimRight(strings.TrimSpace(url), "/"), strings.TrimSpace(name)
		if url == "" || name == "" {
			return nil, fmt.Errorf("AZSP_RESOURCES: %q is not url=name", strings.TrimSpace(entry))
		}
		resources = append(resources, authorization.Resource{URL: url, Name: name})
	}
	if len(resources) == 0 {
		return nil, errors.New("AZSP_RESOURCES names no data service of the ASIP")
	}
	return resources, nil
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

// walletVerifier is the NL Wallet as identification means, when the
// verifier is configured: AZSP_WALLET_VERIFIER_URL (its internal API),
// AZSP_WALLET_VERIFIER_STATUS_URL (its wallet API, where the session status
// is) and the API key in AZSP_WALLET_VERIFIER_API_KEY_FILE. Without the key
// file the wallet is not offered and DigiD (mock) is the only means.
func walletVerifier() (*walletverifier.Client, error) {
	internal := os.Getenv("AZSP_WALLET_VERIFIER_URL")
	if internal == "" {
		return nil, nil
	}
	keyFile := env("AZSP_WALLET_VERIFIER_API_KEY_FILE", "/wallet-verifier/api-key")
	key, err := os.ReadFile(keyFile)
	if errors.Is(err, os.ErrNotExist) {
		slog.Warn("NL Wallet verifier not configured: no API key; logging in with the NL Wallet is not offered", "file", keyFile)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	status := env("AZSP_WALLET_VERIFIER_STATUS_URL", internal)
	return walletverifier.New(internal, status, strings.TrimSpace(string(key))), nil
}
