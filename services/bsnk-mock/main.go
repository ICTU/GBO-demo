// Command bsnk-mock stands in for BSNk, the BSN-koppelregister, and for the
// decryption component a party runs to read what BSNk issued for it.
//
// The code is laid out as ports and adapters:
//
//	internal/polymorphic/  core — BSNk's own model: activate, transform, keys,
//	                       and reading a value. Imports no transport library.
//	internal/legacy/       core — what is left of the first interface: the
//	                       consent portal's own reference to a citizen
//	internal/httpapi/      driving adapter — handlers and routing
//	slogctx.go             trace-correlated access log
//	main.go                configuration, construction, lifecycle
//
// The cores have no driven adapters: the mock keeps nothing and calls nothing.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"

	"bsnk-mock/internal/httpapi"
	"bsnk-mock/internal/polymorphic"
)

// readHeaderTimeout bounds how long a client may take to send its request
// headers, so a stalled connection cannot hold a handler open.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds the drain after SIGTERM: stop accepting, let
// in-flight requests finish, then close whatever is left.
const shutdownTimeout = 15 * time.Second

type config struct {
	Port string
	// BSNAuthorisedOINs are the parties that may receive the BSN. Every
	// other party can only receive a pseudonym.
	BSNAuthorisedOINs []string
	// Randomize makes issued values differ on every request, as they do
	// with the real BSNk. Off by default, so that tests get stable values.
	Randomize bool
	// DecryptionComponentOnly serves the decryption component and nothing of
	// BSNk. A party runs that component itself; an instance started this way
	// stands in for it.
	DecryptionComponentOnly bool
}

func loadConfig() (config, error) {
	cfg := config{Port: getEnv("PORT", "4003")}
	for _, oin := range strings.Split(os.Getenv("BSN_AUTHORISED_OINS"), ",") {
		if oin = strings.TrimSpace(oin); oin != "" {
			cfg.BSNAuthorisedOINs = append(cfg.BSNAuthorisedOINs, oin)
		}
	}
	if v := os.Getenv("RANDOMIZE_VALUES"); v != "" {
		randomize, err := strconv.ParseBool(v)
		if err != nil {
			return config{}, fmt.Errorf("RANDOMIZE_VALUES must be true or false, got %q", v)
		}
		cfg.Randomize = randomize
	}
	if v := os.Getenv("DECRYPTION_COMPONENT_ONLY"); v != "" {
		only, err := strconv.ParseBool(v)
		if err != nil {
			return config{}, fmt.Errorf("DECRYPTION_COMPONENT_ONLY must be true or false, got %q", v)
		}
		cfg.DecryptionComponentOnly = only
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newMux wires the cores to the HTTP adapter.
func newMux(cfg config) (*http.ServeMux, error) {
	if cfg.DecryptionComponentOnly {
		return httpapi.NewDecryptionMux(), nil
	}
	mock, err := polymorphic.New(cfg.BSNAuthorisedOINs)
	if err != nil {
		return nil, fmt.Errorf("BSN_AUTHORISED_OINS: %w", err)
	}
	return httpapi.NewMux(mock, cfg.Randomize), nil
}

func initTracer() func(context.Context) error {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(ctx context.Context) error { return nil }
	}
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "bsnk-mock"
	}
	exp, err := otlptracegrpc.New(context.Background(),
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		slog.Error("otel exporter init failed", "err", err.Error())
		return func(ctx context.Context) error { return nil }
	}
	res, _ := resource.New(context.Background(),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(100*time.Millisecond)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown
}

// fatal logs and ends the process. main is the only place in this service
// that exits; everything else returns an error.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err.Error())
	os.Exit(1)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "bsnk-mock"))

	cfg, err := loadConfig()
	if err != nil {
		fatal("loading configuration from environment", err)
	}
	mux, err := newMux(cfg)
	if err != nil {
		fatal("building the service", err)
	}

	shutdown := initTracer()
	defer func() { _ = shutdown(context.Background()) }()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(withAccessLog(mux), "bsnk-mock"),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	serve(srv)
}

// serve runs the server until the process is asked to stop, then drains it.
// Without this a SIGTERM (docker compose down, a Kubernetes rollout) killed
// in-flight requests outright.
func serve(srv *http.Server) {
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			fatal("listen and serve", err)
		}
	}()
	slog.Info("listening", "addr", srv.Addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	stop()

	slog.Info("shutting down")
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		slog.Warn("drain did not finish; closing remaining connections", "err", err.Error())
		_ = srv.Close()
	}
}
