package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

type Organization struct {
	OIN      string `json:"oin"`
	Name     string `json:"name"`
	Sector   string `json:"sector"`
	KVKSBI   string `json:"kvk_sbi"`
	Register string `json:"register"`
}

var orgsByOIN map[string]Organization

func loadOrganizations() {
	path := os.Getenv("PIP_CONFIG_PATH")
	if path == "" {
		path = "/config/organizations.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("could not load organizations", "path", path, "err", err.Error())
		orgsByOIN = make(map[string]Organization)
		return
	}
	var orgs []Organization
	if err := json.Unmarshal(data, &orgs); err != nil {
		slog.Error("failed to parse organizations.json", "err", err.Error())
		os.Exit(1)
	}
	orgsByOIN = make(map[string]Organization, len(orgs))
	for _, o := range orgs {
		orgsByOIN[o.OIN] = o
	}
	slog.Info("organizations loaded", "count", len(orgs), "path", path)
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

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "sector-pip"))

	shutdown := initTracer()
	defer func() { _ = shutdown(context.Background()) }()

	loadOrganizations()

	mux := newMux(orgsByOIN)

	addr := ":4004"
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, otelhttp.NewHandler(withAccessLog(mux), "sector-pip")); err != nil {
		slog.Error("server error", "err", err.Error())
		os.Exit(1)
	}
}
