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
//	ldv/         driven adapter — the Logboek Dataverwerkingen
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

	"errors"
	"gbo-demo/consent-portal-backend/bsnk"
	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/devportal"
	"gbo-demo/consent-portal-backend/ldv"
	"gbo-demo/consent-portal-backend/logctx"
	"gbo-demo/consent-portal-backend/portalhttp"
	"gbo-demo/consent-portal-backend/register"
	"gbo-demo/consent-portal-backend/upstream"
	"net"
	"os/signal"
	"syscall"
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

// readHeaderTimeout bounds how long a client may take to send its request
// headers, so a stalled connection cannot hold a handler open.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds the drain after SIGTERM: stop accepting, let
// in-flight requests finish, then close whatever is left.
const shutdownTimeout = 15 * time.Second

// streamGrace is how long ordinary in-flight requests get to finish before
// the SSE streams are ended. Shutdown waits for active requests but does not
// cancel their contexts, so /portal/events would otherwise hold the drain
// open for the full shutdownTimeout on every restart.
const streamGrace = 2 * time.Second

// ── Config ────────────────────────────────────────────────────────────────

type config struct {
	Port             string
	BSNkURL          string
	ConsentURL       string
	DevPortalBackend string
	// LogbookURL empty means this portal is not part of an LDV chain and
	// writes no Dataverwerkingen records.
	LogbookURL      string
	LogbookToken    string
	LogbookFallback string
}

func loadConfig() config {
	return config{
		Port:             getEnv("PORT", "4005"),
		BSNkURL:          getEnv("BSNK_URL", "http://bsnk-mock:4003"),
		ConsentURL:       getEnv("CONSENT_URL", "http://consent-register:4002"),
		DevPortalBackend: getEnv("DEV_PORTAL_BACKEND_URL", ""),
		LogbookURL:       getEnv("LDV_LOGBOOK_URL", ""),
		LogbookToken:     getEnv("LDV_WRITE_TOKEN", ""),
		LogbookFallback:  getEnv("LDV_FALLBACK_ACTIVITY", ""),
	}
}

// How long to wait for the logbook at startup. A component that must log
// cannot start without one; compose orders it after the logbook's health
// check, and the retries only cover the gap on a cold stack.
const (
	ldvRegisterAttempts = 15
	ldvRegisterBackoff  = time.Second
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── Wiring ────────────────────────────────────────────────────────────────

// newPortal builds the core with its production adapters. This is the only
// place in the service that names both a concrete adapter and the core.
func newPortal(cfg config, hub *portalhttp.Hub, logbook consent.Logbook) *consent.Portal {
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
		Logbook:    logbook,
		OwnOIN:     portalOIN,
	}
}

// newLogbook builds the Logboek Dataverwerkingen adapter, or nothing when
// this deployment is not part of an LDV chain. A typed nil would satisfy the
// interface while being nil underneath, so the concrete absence is turned
// into an interface-level one here.
func newLogbook(cfg config, serviceName string) (consent.Logbook, *ldv.Logbook, error) {
	writer, err := ldv.New(serviceName, cfg.LogbookURL, cfg.LogbookToken, cfg.LogbookFallback)
	if err != nil {
		return nil, nil, err
	}
	if writer == nil {
		return nil, nil, nil
	}
	return writer, writer, nil
}

// newMux wires the core to its production adapters and builds the routing
// tree. Extracted from main so integration tests can drive the real handlers
// through an httptest.Server without starting the listener.
func newMux(cfg config, hub *portalhttp.Hub, logbook consent.Logbook) *http.ServeMux {
	return portalhttp.NewMux(newPortal(cfg, hub, logbook), hub)
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
	serviceName := getEnv("OTEL_SERVICE_NAME", "consent-portal-backend")
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", serviceName))

	shutdown := initTracer()
	defer func() { _ = shutdown(context.Background()) }()

	cfg := loadConfig()
	hub := portalhttp.NewHub()

	// Either this portal is part of GBO's LDV chain and cannot start without
	// its logbook, or it is not and writes no records.
	logbook, writer, err := newLogbook(cfg, serviceName)
	if err != nil {
		fatal("configuring the logboek adapter", err)
	}
	if writer != nil {
		if err := writer.LoadRegister(context.Background(), ldvRegisterAttempts, ldvRegisterBackoff); err != nil {
			fatal("reading the logboek verwerkingsactiviteiten register", err)
		}
		slog.Info("logboek configured", "fallback_activity", writer.Fallback())
	} else {
		slog.Warn("no LDV_LOGBOOK_URL configured; this portal writes no Logboek Dataverwerkingen records")
	}

	// BaseContext gives every request a context this process can cancel, which
	// is how the long-lived SSE streams are told to wind up at shutdown.
	baseCtx, endStreams := context.WithCancel(context.Background())
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(portalhttp.WithDemoSession(logctx.WithAccessLog(newMux(cfg, hub, logbook))), serviceName),
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}
	slog.Info("listening", "addr", srv.Addr)
	serve(srv, endStreams)
}

// fatal logs and ends the process. main is the only place in this service
// that exits; everything else returns an error.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err.Error())
	os.Exit(1)
}

// serve runs the server until the process is asked to stop, then drains it.
// Without this a SIGTERM (docker compose down, a Kubernetes rollout) killed
// in-flight consent writes outright. Previously a failed ListenAndServe was
// only logged, so a service that could not bind its port exited 0.
func serve(srv *http.Server, endStreams context.CancelFunc) {
	go func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			fatal("listen and serve", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	stop()

	slog.Info("shutting down")
	// Let ordinary requests finish first, then release the SSE streams.
	time.AfterFunc(streamGrace, endStreams)
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		slog.Warn("drain did not finish; closing remaining connections", "err", err.Error())
		_ = srv.Close()
	}
}
