// Command eudi-credential-issuer is a credential issuer for EUDI wallets
// (OpenID4VCI 1.0, HAIP 1.0). See README.md.
//
// Usage:
//
//	eudi-credential-issuer [serve]   run the issuer
//	eudi-credential-issuer migrate   apply database migrations and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/config"
	"gbo-demo/eudi-credential-issuer/internal/httpapi"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/login"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/eudi-credential-issuer/internal/postgres"
	"gbo-demo/eudi-credential-issuer/internal/source"
	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/statuslist"
)

const defaultSchema = "eudi_credential_issuer"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "eudi-credential-issuer")
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch cmd {
	case "serve":
		err = serve(ctx, log)
	case "migrate":
		err = migrate(ctx)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		log.Error("exit", "error", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func migrate(ctx context.Context) error {
	store, err := postgres.Open(ctx, os.Getenv("ISSUER_DATABASE_URL"), env("ISSUER_DATABASE_SCHEMA", defaultSchema))
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Migrate(ctx)
}

// store is what the issuer needs from its state store.
type store interface {
	issuance.Sessions
	issuance.OneTime
	issuance.StatusLists
}

func serve(ctx context.Context, log *slog.Logger) error {
	loaded, err := config.Load(os.Getenv("ISSUER_CONFIG_FILE"), os.Getenv("ISSUER_PUBLIC_URL"))
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	cfg := loaded.Issuer
	var st store
	if os.Getenv("ISSUER_STORE") == "memory" {
		// Demo shortcut for a local run: state is lost on restart and not
		// shared between replicas.
		log.Warn("using the in-memory store")
		st = memstore.New()
	} else {
		pg, err := postgres.Open(ctx, os.Getenv("ISSUER_DATABASE_URL"), env("ISSUER_DATABASE_SCHEMA", defaultSchema))
		if err != nil {
			return err
		}
		defer pg.Close()
		go purge(ctx, log, pg, cfg.TokenTTL)
		st = pg
	}
	src := &source.Client{HTTP: &http.Client{Timeout: 10 * time.Second}, Endpoints: loaded.SourceEndpoints, Clients: map[string]*http.Client{}}
	for id, roots := range loaded.SourceRoots {
		src.Clients[id] = source.WithRoots(src.HTTP, roots)
	}
	cfg.FetchPIDStatus = pidStatusFetcher(&http.Client{Timeout: 10 * time.Second})
	if err := configureLogin(&cfg, log); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	svc, err := issuance.New(cfg, src, st, st, st, time.Now)
	if err != nil {
		return err
	}

	public := &http.Server{Addr: env("ISSUER_LISTEN_ADDR", ":8080"), Handler: httpapi.Public(svc, log), ReadHeaderTimeout: 10 * time.Second}
	internal := &http.Server{Addr: env("ISSUER_INTERNAL_ADDR", ":8081"), Handler: httpapi.Internal(svc, log), ReadHeaderTimeout: 10 * time.Second}
	errs := make(chan error, 2)
	for _, srv := range []*http.Server{public, internal} {
		go func() {
			log.Info("listening", "addr", srv.Addr)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}
	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return errors.Join(public.Shutdown(shutdown), internal.Shutdown(shutdown))
}

// configureLogin sets up the authorization code flow when
// ISSUER_LOGIN_SERVER names GBO's authorization server: each provider becomes
// its client, with its access certificate and its entry
// ISSUER_LOGIN_REGISTER_BASE/<provider> in the register of wallet-relying
// parties, and the eudi profiles accept the wallet redirect URIs of
// ISSUER_WALLET_REDIRECT_URIS. ISSUER_LOGIN_URL_MAP maps the server's public
// address to one this container reaches ("public=internal"). Without an eudi
// profile there is nothing to log in for: the login is off, and the nl
// profile still issues.
func configureLogin(cfg *issuance.Config, log *slog.Logger) error {
	server := os.Getenv("ISSUER_LOGIN_SERVER")
	if server == "" {
		return nil
	}
	var eudi string
	for i, p := range cfg.Profiles {
		if p.Kind != issuance.KindEUDI {
			continue
		}
		cfg.Profiles[i].WalletRedirectURIs = splitList(env("ISSUER_WALLET_REDIRECT_URIS", "eu.europa.ec.euidi://authorization"))
		if eudi == "" {
			eudi = p.Name
		}
	}
	if eudi == "" {
		log.Warn("login is off: ISSUER_LOGIN_SERVER is set, but there is no eudi profile")
		return nil
	}
	transport, err := login.NewURLMap(os.Getenv("ISSUER_LOGIN_URL_MAP"), http.DefaultTransport)
	if err != nil {
		return err
	}
	base := strings.TrimRight(env("ISSUER_LOGIN_REGISTER_BASE", "https://register.gbo.example/wrp"), "/")
	var providers []login.Provider
	for _, p := range cfg.Providers {
		providers = append(providers, login.Provider{Name: p.Name, Key: p.AccessKey, Chain: p.AccessChain,
			RegistryURI: base + "/" + p.Name, RedirectURI: strings.TrimRight(cfg.PublicURL, "/") + "/" + p.Name + "/" + eudi + "/callback"})
	}
	client, err := login.New(server, &http.Client{Timeout: 10 * time.Second, Transport: transport}, providers)
	if err != nil {
		return err
	}
	cfg.Login = client
	return nil
}

func splitList(value string) []string {
	var out []string
	for _, v := range strings.Split(value, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// pidStatusFetcher fetches the status lists of disclosed PIDs. The URI
// comes from a PID whose issuer was verified; still, only https is
// fetched and redirects are not followed.
func pidStatusFetcher(base *http.Client) oid4vp.StatusFetcher {
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return func(ctx context.Context, uri string) (string, error) {
		if u, err := url.Parse(uri); err != nil || u.Scheme != "https" {
			return "", fmt.Errorf("status list %q is not an https URL", uri)
		}
		return statuslist.Fetch(ctx, &client, uri)
	}
}

// purge removes expired state every minute. Every replica may run it; the
// deletes are idempotent.
func purge(ctx context.Context, log *slog.Logger, pg *postgres.Store, tokenTTL time.Duration) {
	if tokenTTL == 0 {
		tokenTTL = issuance.DefaultTokenTTL
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := pg.Purge(ctx, now, tokenTTL); err != nil {
				log.Warn("purge failed", "error", err)
			}
		}
	}
}
