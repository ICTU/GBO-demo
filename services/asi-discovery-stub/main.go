// Command asi-discovery-stub is a stub of the Discover Interface Provider of
// ETSI TS 119 478 clause 5: an external role (the Data Service Directory of
// the common services), stubbed so a QTSP can discover the demo's ASIP. It
// answers the retrieve query (clause 5.3) from the dataService records the
// configured ASIPs publish per data service; it keeps no records itself and
// has no semantic repository (search is 501).
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
	"strings"
	"syscall"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "asi-discovery-stub"))
	if err := serve(); err != nil {
		slog.Error("serving asi-discovery-stub", "err", err)
		os.Exit(1)
	}
}

func serve() error {
	var sources []string
	for _, s := range strings.Split(os.Getenv("DIP_SOURCES"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			sources = append(sources, s)
		}
	}
	if len(sources) == 0 {
		return errors.New("DIP_SOURCES is required: the URLs of the ASIP data services' records, separated by commas")
	}
	dir, err := newDirectory(sources)
	if err != nil {
		return err
	}
	handler, err := newHandler(dir)
	if err != nil {
		return err
	}
	addr := os.Getenv("DIP_ADDR")
	if addr == "" {
		addr = ":4022"
	}
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr, "sources", len(sources))
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
