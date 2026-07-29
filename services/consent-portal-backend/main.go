// Package main is the composition root of the citizen-facing consent portal
// backend. It owns the BSN boundary on the citizen side: the citizen-facing
// frontend (toestemmingsportaal-frontend :9002) talks to this service over a
// token, not by sending a plain BSN as a JSON field. The portal performs the
// BSNk pseudonymisation and registers the consent in the consent register
// with PI as subject — the register never sees a plain BSN.
//
// The code is laid out as ports and adapters, one package per dependency:
//
//	consent/     domain core — the BSN/PI boundary and every consent rule.
//	             Imports no transport library; the compiler enforces it.
//	bsnk/        driven adapter — BSNk pseudonymisation
//	register/    driven adapter — the consent register
//	devportal/   driven adapter — best-effort history, shaped as an Observer
//	upstream/    the shared JSON caller those adapters use
//	portalhttp/  driving adapters — handlers, JWT, SSE, routing
//	logctx/      trace-correlated logging
//	main.go      wiring, and nothing else
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

	"gbo-demo/consent-portal-backend/bsnk"
	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/devportal"
	"gbo-demo/consent-portal-backend/logctx"
	"gbo-demo/consent-portal-backend/portalhttp"
	"gbo-demo/consent-portal-backend/register"
	"gbo-demo/consent-portal-backend/upstream"
)

// portalOIN is the portal's own OIN, used as recipient_oin when it needs the
// caller's PI for its own sake (listing, ownership checks). The PI BSNk
// returns is deterministic per BSN regardless of recipient_oin.
const portalOIN = "00000000000000000002" // mock-portal OIN

// upstreamTimeout bounds every call to BSNk and the consent register. The
// previous implementation used the package-global http.DefaultClient, which
// has no timeout at all, so an unresponsive upstream would hang a citizen's
// request indefinitely.
const upstreamTimeout = 10 * time.Second

// historyTimeout bounds the best-effort dev-portal history post.
const historyTimeout = 3 * time.Second

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

// newPortal builds the core with its production adapters. This is the only
// place in the service that names both a concrete adapter and the core.
func newPortal(cfg config, hub *portalhttp.Hub) *consent.Portal {
	caller := upstream.Caller{Client: &http.Client{Timeout: upstreamTimeout}}

	// Who is watching this flow. Order is irrelevant; each observer reads
	// only the event field it cares about.
	var watchers consent.FanOut
	if hub != nil {
		watchers = append(watchers, hub)
	}
	if cfg.DevPortalBackend != "" {
		watchers = append(watchers, &devportal.History{
			Base:   cfg.DevPortalBackend,
			Client: &http.Client{Timeout: historyTimeout},
		})
	}

	return &consent.Portal{
		Pseudonyms: bsnk.Client{Base: cfg.BSNkURL, Caller: caller},
		Consents:   register.Client{Base: cfg.ConsentURL, Caller: caller},
		Watch:      watchers,
		OwnOIN:     portalOIN,
	}
}

// newMux wires the core to its production adapters and builds the routing
// tree. Extracted from main so integration tests can drive the real handlers
// through an httptest.Server without starting the listener.
func newMux(cfg config, hub *portalhttp.Hub) *http.ServeMux {
	return portalhttp.NewMux(newPortal(cfg, hub), hub)
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
	hub := portalhttp.NewHub()
	mux := newMux(cfg, hub)

	addr := ":" + cfg.Port
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, otelhttp.NewHandler(logctx.WithAccessLog(mux), "consent-portal-backend")); err != nil {
		slog.Error("server error", "err", err.Error())
		os.Exit(1)
	}
}
