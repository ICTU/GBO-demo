// Command consent-usage-index is GBO's consent usage index: under consent X,
// source Y delivered data under trace Z, at time T. Sources register a trace
// after each delivery under a consent; the consent register looks up the
// registrations of one consent to show the citizen when it was used (#687,
// which calls it the trace index).
//
// It runs under the consent register's organisation (Peer ID ...0700), next
// to the register, and is published over FSC through cr-inway. This file is
// the composition root.
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

	"consent-usage-index/internal/httpapi"
	"consent-usage-index/internal/sqlite"
	"consent-usage-index/internal/usage"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp/filters"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 15 * time.Second
)

type config struct {
	FSCPort      string
	InternalPort string
	DatabasePath string
	ReadToken    string
}

func loadConfig() config {
	return config{
		FSCPort:      getEnv("FSC_PORT", "4019"),
		InternalPort: getEnv("INTERNAL_PORT", "4020"),
		DatabasePath: getEnv("DATABASE_PATH", "/data/consent-usage-index.db"),
		ReadToken:    os.Getenv("INDEX_READ_TOKEN"),
	}
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// initTracer lets the SDK read OTEL_EXPORTER_OTLP_* itself (no
// WithEndpoint), so the same binary works under compose and under Aspire.
func initTracer(ctx context.Context) func(context.Context) error {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return func(context.Context) error { return nil }
	}
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		slog.Warn("tracer init failed", "err", err.Error())
		return func(context.Context) error { return nil }
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(100*time.Millisecond)))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return provider.Shutdown
}

func main() {
	serviceName := getEnv("OTEL_SERVICE_NAME", "consent-usage-index")
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", serviceName))
	cfg := loadConfig()

	// Without a read token the lookup would be open to every service on the
	// network; refuse to start rather than serve it that way.
	if cfg.ReadToken == "" {
		slog.Error("INDEX_READ_TOKEN is required")
		os.Exit(1)
	}

	shutdownTracer := initTracer(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracer(ctx)
	}()

	store, err := sqlite.Open(cfg.DatabasePath)
	if err != nil {
		slog.Error("opening the store", "err", err.Error())
		os.Exit(1)
	}
	defer func() { _ = store.Close() }()
	index := usage.New(store, nil)

	notHealth := otelhttp.WithFilter(filters.Not(filters.Path("/health")))
	servers := []*http.Server{
		{
			Addr:              ":" + cfg.FSCPort,
			Handler:           otelhttp.NewHandler(httpapi.NewFSCHandler(index, store), serviceName, notHealth),
			ReadHeaderTimeout: readHeaderTimeout,
		},
		{
			Addr:              ":" + cfg.InternalPort,
			Handler:           otelhttp.NewHandler(httpapi.NewInternalHandler(index, store, cfg.ReadToken), serviceName, notHealth),
			ReadHeaderTimeout: readHeaderTimeout,
		},
	}
	serve(servers...)
}

// serve runs the listeners until SIGTERM, then drains them.
func serve(servers ...*http.Server) {
	for _, srv := range servers {
		go func(srv *http.Server) {
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				slog.Error("listen and serve", "addr", srv.Addr, "err", err.Error())
				os.Exit(1)
			}
		}(srv)
		slog.Info("listening", "addr", srv.Addr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	stop()

	slog.Info("shutting down")
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(drainCtx); err != nil {
			_ = srv.Close()
		}
	}
}
