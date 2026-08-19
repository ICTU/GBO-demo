package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	Port                 string
	DatabasePath         string
	OnboardingConfigPath string
	SeedDemoData         bool
}

func loadConfig() config {
	seed, _ := strconv.ParseBool(getEnv("SEED_DEMO_DATA", "false"))
	return config{
		Port:                 getEnv("PORT", "4015"),
		DatabasePath:         getEnv("DATABASE_PATH", "/data/onboarding.db"),
		OnboardingConfigPath: getEnv("ONBOARDING_CONFIG_PATH", "/config/onboarding.json"),
		SeedDemoData:         seed,
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func generateCSRFToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
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

	onboardingConfiguration, err := onboarding.LoadConfiguration(cfg.OnboardingConfigPath)
	if err != nil {
		slog.Error("loading onboarding configuration", "err", err)
		os.Exit(1)
	}
	service, err := onboarding.NewService(repository, onboardingConfiguration)
	if err != nil {
		slog.Error("validating onboarding configuration", "err", err)
		os.Exit(1)
	}
	if cfg.SeedDemoData {
		if err := service.SeedDemo(context.Background()); err != nil {
			slog.Error("seeding onboarding register", "err", err)
			os.Exit(1)
		}
	}
	csrfToken, err := generateCSRFToken()
	if err != nil {
		slog.Error("generating CSRF token", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.NewHandler(service, csrfToken),
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
