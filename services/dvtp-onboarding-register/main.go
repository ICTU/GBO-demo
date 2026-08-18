package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"dvtp-onboarding-register/internal/httpapi"
	"dvtp-onboarding-register/internal/onboarding"
	"dvtp-onboarding-register/internal/sqlite"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

type config struct {
	Port         string
	DatabasePath string
	SeedDemoData bool
}

func loadConfig() config {
	seed, _ := strconv.ParseBool(getEnv("SEED_DEMO_DATA", "false"))
	return config{
		Port:         getEnv("PORT", "4015"),
		DatabasePath: getEnv("DATABASE_PATH", "/data/onboarding.db"),
		SeedDemoData: seed,
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "dvtp-onboarding-register"))
	cfg := loadConfig()
	repository, err := sqlite.Open(cfg.DatabasePath)
	if err != nil {
		slog.Error("opening onboarding register", "err", err)
		os.Exit(1)
	}
	defer func() { _ = repository.Close() }()

	service := onboarding.NewService(repository)
	if cfg.SeedDemoData {
		if err := service.SeedDemo(context.Background()); err != nil {
			slog.Error("seeding onboarding register", "err", err)
			os.Exit(1)
		}
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.NewHandler(service),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	go func() {
		slog.Info("listening", "addr", srv.Addr, "database", cfg.DatabasePath)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serving onboarding register", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		slog.Warn("graceful shutdown did not finish", "err", err)
	}
}
