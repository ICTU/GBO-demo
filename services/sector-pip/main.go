package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
)

// readHeaderTimeout bounds how long a client may take to send its request
// headers, so a stalled connection cannot hold a handler open.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds the drain after SIGTERM: stop accepting, let
// in-flight requests finish, then close whatever is left.
const shutdownTimeout = 15 * time.Second

type Organization struct {
	OIN      string `json:"oin"`
	Name     string `json:"name"`
	Sector   string `json:"sector"`
	KVKSBI   string `json:"kvk_sbi"`
	Register string `json:"register"`
}

type config struct {
	Port       string
	ConfigPath string
}

func loadConfig() (config, error) {
	return config{
		Port:       getEnv("PORT", "4004"),
		ConfigPath: getEnv("PIP_CONFIG_PATH", "/config/organizations.json"),
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadOrganizations reads the register and returns it indexed by OIN. It
// returns an error rather than exiting so the failure path is testable and so
// main stays the only place that can end the process.
//
// A missing file is fatal: a PIP serving an empty register answers "OIN not
// found" for every lookup while still passing its health check, which is
// worse than not starting.
func loadOrganizations(path string) (map[string]Organization, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var orgs []Organization
	if err := json.Unmarshal(data, &orgs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	byOIN := make(map[string]Organization, len(orgs))
	for _, o := range orgs {
		byOIN[o.OIN] = o
	}
	slog.Info("organizations loaded", "count", len(orgs), "path", path)
	return byOIN, nil
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func initTracer() func(context.Context) error {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(ctx context.Context) error { return nil }
	}
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "sector-pip"
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

// newMux builds the routing tree with the given organizations index.
// Extracted from main so integration tests can wire the handlers to an
// httptest.Server with fixture data without starting the real listener
// or reading the on-disk config file.
func newMux(orgs map[string]Organization) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// GET /organizations - list all organizations
	mux.HandleFunc("/organizations", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		out := make([]Organization, 0, len(orgs))
		for _, o := range orgs {
			out = append(out, o)
		}
		writeJSON(w, http.StatusOK, out)
	})

	// GET /organizations/{oin} - look up org by OIN
	mux.HandleFunc("/organizations/", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		oin := strings.TrimPrefix(r.URL.Path, "/organizations/")
		if oin == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing OIN"})
			return
		}
		org, ok := orgs[oin]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"valid": "false",
				"error": "OIN not found in register",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"valid":    true,
			"oin":      org.OIN,
			"name":     org.Name,
			"sector":   org.Sector,
			"kvk_sbi":  org.KVKSBI,
			"register": org.Register,
		})
	})

	return mux
}

// fatal logs and ends the process. main is the only place in this service
// that exits; everything else returns an error.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err.Error())
	os.Exit(1)
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "sector-pip"))

	cfg, err := loadConfig()
	if err != nil {
		fatal("loading configuration from environment", err)
	}

	shutdown := initTracer()
	defer func() { _ = shutdown(context.Background()) }()

	orgs, err := loadOrganizations(cfg.ConfigPath)
	if err != nil {
		fatal("loading organizations", err)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(withAccessLog(newMux(orgs)), "sector-pip"),
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
