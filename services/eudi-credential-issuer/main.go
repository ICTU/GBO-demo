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
	"os"
	"os/signal"
	"syscall"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/config"
	"gbo-demo/eudi-credential-issuer/internal/httpapi"
	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/eudi-credential-issuer/internal/memstore"
	"gbo-demo/eudi-credential-issuer/internal/postgres"
	"gbo-demo/eudi-credential-issuer/internal/source"
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
	cfg, err := config.Load(os.Getenv("ISSUER_CONFIG_FILE"), os.Getenv("ISSUER_PUBLIC_URL"))
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
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
	src := &source.Client{HTTP: &http.Client{Timeout: 10 * time.Second}}
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
