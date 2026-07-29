// Package main implements the citizen-facing consent portal backend as a
// separate component. It owns the BSN boundary on the citizen side: the
// citizen-facing frontend (toestemmingsportaal-frontend :9002) talks to
// this service over a token, not by sending a plain BSN as a JSON field.
// The portal performs the BSNk pseudonymisation and registers the consent
// in the consent register with PI as subject — the register never sees a
// plain BSN.
//
// The code is laid out as ports and adapters:
//
//	portal.go    domain core — the BSN/PI boundary and every consent rule
//	adapters.go  driven adapters — BSNk and the consent register over HTTP
//	httpapi.go   driving adapters — handlers, JWT, routing
//	observers.go the watchers: SSE panel, call cards, dev-portal history
//	main.go      composition root — config, tracing, wiring
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

// ── Config ────────────────────────────────────────────────────────────────

type config struct {
	Port             string
	BSNkURL          string
	ConsentURL       string
	DevPortalBackend string
}

func loadConfig() config {
	return config{
		Port:             getEnv("PORT", "4005"),
		BSNkURL:          getEnv("BSNK_URL", "http://bsnk-mock:4003"),
		ConsentURL:       getEnv("CONSENT_URL", "http://consent-register:4002"),
		DevPortalBackend: getEnv("DEV_PORTAL_BACKEND_URL", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Wiring ────────────────────────────────────────────────────────────────

// upstreamTimeout bounds every call to BSNk and the consent register. The
// previous implementation used the package-global http.DefaultClient, which
// has no timeout at all, so an unresponsive upstream would hang a citizen's
// request indefinitely.
const upstreamTimeout = 10 * time.Second

// historyTimeout bounds the best-effort dev-portal history post.
const historyTimeout = 3 * time.Second

// newPortal builds the core with its production adapters. This is the only
// place in the service that names both a concrete adapter and the core.
func newPortal(cfg config, hub *SSEHub) *Portal {
	shared := caller{client: &http.Client{Timeout: upstreamTimeout}}

	// Who is watching this flow. Order is irrelevant; each observer reads
	// only the event field it cares about.
	var watchers FanOut
	if hub != nil {
		watchers = append(watchers, hub)
	}
	if cfg.DevPortalBackend != "" {
		watchers = append(watchers, &historyObserver{
			base: cfg.DevPortalBackend,
			http: &http.Client{Timeout: historyTimeout},
		})
	}

	return &Portal{
		Pseudonyms: bsnkClient{base: cfg.BSNkURL, caller: shared},
		Consents:   registerClient{base: cfg.ConsentURL, caller: shared},
		Watch:      watchers,
		OwnOIN:     portalOIN,
	}
}

// ── OTel setup ────────────────────────────────────────────────────────────

func initTracer() func(context.Context) error {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		otel.SetTextMapPropagator(propagation.TraceContext{})
		return func(ctx context.Context) error { return nil }
	}
	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "consent-portal-backend"
	}
	exp, err := otlptracegrpc.New(context.Background(),
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		slog.Error("otel exporter init failed", "err", err.Error())
		otel.SetTextMapPropagator(propagation.TraceContext{})
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

// ── Main ──────────────────────────────────────────────────────────────────

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "consent-portal-backend"))

	shutdown := initTracer()
	defer func() { _ = shutdown(context.Background()) }()

	cfg := loadConfig()
	hub := NewSSEHub()
	mux := newMux(cfg, hub)

	addr := ":" + cfg.Port
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, otelhttp.NewHandler(withAccessLog(mux), "consent-portal-backend")); err != nil {
		slog.Error("server error", "err", err.Error())
		os.Exit(1)
	}
}
