// Command ldv-logboek is the Logboek Dataverwerkingen of one Verantwoordelijke.
//
// One image, one instance per Verantwoordelijke — the same pattern as the
// sidecars. LDV is explicit that each Verantwoordelijke logs its own
// processing and that only trace metadata crosses a boundary, so a shared
// logbook would be the wrong shape however convenient it looks in a demo.
// Which organisation an instance belongs to comes from its register document
// and its environment; the code knows nothing about Belastingdienst.
//
// This file is the composition root: configuration, construction, lifecycle.
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

	"ldv-logboek/internal/httpapi"
	"ldv-logboek/internal/ldv"
	"ldv-logboek/internal/sqlite"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

type config struct {
	Port         string
	DatabasePath string
	RegisterPath string
	WriteToken   string
	ReadToken    string
}

func loadConfig() config {
	return config{
		Port:         getEnv("PORT", "4016"),
		DatabasePath: getEnv("DATABASE_PATH", "/data/logboek.db"),
		RegisterPath: getEnv("REGISTER_PATH", "/config/verwerkingsactiviteiten.json"),
		WriteToken:   os.Getenv("LDV_WRITE_TOKEN"),
		ReadToken:    os.Getenv("LDV_READ_TOKEN"),
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	serviceName := getEnv("OTEL_SERVICE_NAME", "ldv-logboek")
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", serviceName))
	cfg := loadConfig()

	// No token, no logbook. The write endpoint is the only way records get in;
	// starting without one would quietly turn a Verantwoordelijke's logboek
	// into a write-anything endpoint on the internal network.
	if cfg.WriteToken == "" {
		slog.Error("LDV_WRITE_TOKEN is required")
		os.Exit(1)
	}
	// Reading a logbook is a different capability from writing one: every
	// instrumented component writes, and almost nothing should read. Without
	// a read token the extensie lezen simply is not served, rather than being
	// served to whoever holds the write token.
	if cfg.ReadToken == "" {
		slog.Warn("no LDV_READ_TOKEN configured; the read extension will refuse every request")
	}

	register, err := ldv.LoadRegister(cfg.RegisterPath)
	if err != nil {
		slog.Error("loading verwerkingsactiviteiten register", "err", err)
		os.Exit(1)
	}

	repository, err := sqlite.Open(cfg.DatabasePath)
	if err != nil {
		slog.Error("opening logboek store", "err", err)
		os.Exit(1)
	}
	defer func() { _ = repository.Close() }()

	logbook, err := ldv.NewLogbook(repository, register, time.Now)
	if err != nil {
		slog.Error("wiring logbook", "err", err)
		os.Exit(1)
	}

	stored, err := repository.Count(context.Background())
	if err != nil {
		slog.Error("reading logboek store", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.NewHandler(logbook, cfg.WriteToken, cfg.ReadToken),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	go func() {
		slog.Info("logboek listening",
			"addr", srv.Addr,
			"verantwoordelijke", register.Verantwoordelijke,
			"verwerkingsactiviteiten", len(register.References()),
			"records_on_disk", stored,
			"database", cfg.DatabasePath,
		)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serving logboek", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	stop()

	slog.Info("shutting down")
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		slog.Warn("drain did not finish; closing remaining connections", "err", err)
		_ = srv.Close()
	}
}
