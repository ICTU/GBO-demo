// Command eudi-wallet-verifier is the verifier with which the ASI
// authorization server has the citizen disclose the PID from an EUDI
// wallet (#639). It offers the part of nl-wallet's verification_server API
// the authorization server uses, so the server only gets a second verifier
// configured; the NL Wallet keeps asi-wallet-verifier.
//
// Configuration: the file at EUDI_WALLET_VERIFIER_CONFIG (default
// /config/config.json). The wallet API listens on :8001, the internal API,
// behind the API key, on :8002.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"gbo-demo/eudi-wallet-verifier/internal/verifier"
	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/statuslist"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("eudi-wallet-verifier stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	path := os.Getenv("EUDI_WALLET_VERIFIER_CONFIG")
	if path == "" {
		path = "/config/config.json"
	}
	cfg, err := verifier.Load(path)
	if err != nil {
		return err
	}
	svc := verifier.New(cfg, statusFetcher(&http.Client{Timeout: 10 * time.Second}), nil)
	errs := make(chan error, 2)
	for _, l := range []struct {
		addr    string
		handler http.Handler
	}{
		{":8001", verifier.PublicHandler(svc, log)},
		{":8002", verifier.InternalHandler(svc, log)},
	} {
		srv := &http.Server{Addr: l.addr, Handler: l.handler, ReadHeaderTimeout: 10 * time.Second}
		go func() { errs <- srv.ListenAndServe() }()
	}
	log.Info("eudi-wallet-verifier listening", "public", cfg.PublicURL, "usecases", len(cfg.Usecases))
	err = <-errs
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// statusFetcher fetches a PID's status list over https only, without
// following redirects.
func statusFetcher(base *http.Client) oid4vp.StatusFetcher {
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return func(ctx context.Context, uri string) (string, error) {
		if u, err := url.Parse(uri); err != nil || u.Scheme != "https" {
			return "", fmt.Errorf("status list %q is not an https URL", uri)
		}
		return statuslist.Fetch(ctx, &client, uri)
	}
}
