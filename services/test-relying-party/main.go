// Command test-relying-party is a test relying party for the demo: it asks
// wallets for an attestation over OpenID4VP on behalf of a few registered
// relying parties, and records which copy was presented to whom. See
// README.md.
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

	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/test-relying-party/internal/rp"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "test-relying-party")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx, log); err != nil {
		log.Error("stopped", "error", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func serve(ctx context.Context, log *slog.Logger) error {
	cfg, err := rp.Load(os.Getenv("RP_CONFIG_FILE"))
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	fetch, err := statusFetcher(&http.Client{Timeout: 10 * time.Second}, os.Getenv("RP_STATUS_URL_MAP"))
	if err != nil {
		return fmt.Errorf("RP_STATUS_URL_MAP: %w", err)
	}
	svc := rp.New(cfg, fetch, time.Now)
	public := &http.Server{Addr: env("RP_LISTEN_ADDR", ":8080"), Handler: rp.Public(svc, log), ReadHeaderTimeout: 10 * time.Second}
	internal := &http.Server{Addr: env("RP_INTERNAL_ADDR", ":8081"), Handler: rp.Internal(svc), ReadHeaderTimeout: 10 * time.Second}
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

// statusFetcher fetches the status lists of presented credentials. Only
// https is fetched and redirects are not followed. mapping holds
// "public=internal" pairs, comma-separated: a status list under a public
// prefix is fetched from the internal one, so the container does not go
// out through the tunnel for the issuer next to it.
func statusFetcher(base *http.Client, mapping string) (oid4vp.StatusFetcher, error) {
	type pair struct{ public, internal string }
	var pairs []pair
	for _, p := range strings.Split(mapping, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		public, internal, ok := strings.Cut(p, "=")
		if !ok || public == "" || internal == "" {
			return nil, fmt.Errorf("%q is not public=internal", p)
		}
		pairs = append(pairs, pair{strings.TrimRight(public, "/"), strings.TrimRight(internal, "/")})
	}
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return func(ctx context.Context, uri string) (string, error) {
		if u, err := url.Parse(uri); err != nil || u.Scheme != "https" {
			return "", fmt.Errorf("status list %q is not an https URL", uri)
		}
		target := uri
		for _, p := range pairs {
			if strings.HasPrefix(uri, p.public+"/") {
				target = p.internal + strings.TrimPrefix(uri, p.public)
				break
			}
		}
		return statuslist.Fetch(ctx, &client, target)
	}, nil
}
