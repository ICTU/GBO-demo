// Command test-pid-source is the bronpoort of the test PID that the own
// credential issuer gives to wallets without a real PID (the EUDI
// reference wallet). It answers the IssuableDocument contract of the
// issuer for {"subject": {"bsn": ...}} with a PID of type urn:eudi:pid:nl:1,
// built from the BRP mock persons. A test fixture: it is no PID Provider
// and must only be reachable inside the demo network.
//
// This file is the composition root.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "test-pid-source"))
	if err := serve(); err != nil {
		slog.Error("serving test-pid-source", "err", err)
		os.Exit(1)
	}
}

func serve() error {
	path := os.Getenv("PERSONS_FILE")
	if path == "" {
		return errors.New("PERSONS_FILE is required: the BRP mock persons (brp-graphql-server/mockdata/personen.json)")
	}
	persons, err := loadPersons(path)
	if err != nil {
		return err
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{Addr: addr, Handler: newHandler(persons, time.Now), ReadHeaderTimeout: readHeaderTimeout}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr, "persons", len(persons))
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
	return srv.Shutdown(drainCtx)
}
