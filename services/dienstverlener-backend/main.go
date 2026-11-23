// Package main is the composition root of the demo consumer backend — the
// server-side component of a fictive data consumer (Hypotheek-BV, or the
// Installatie Register with QUERY_KIND=lvg). It owns the FSC-client boundary:
// the browser frontend only talks to this backend, never to FSC or the PEP
// directly. In a production deployment this service would hold mTLS keys and
// FSC Outway config.
//
// The code is laid out as ports and adapters, one package per dependency:
//
//	consumer/        domain core — the question, the consent token's claims,
//	                 what a citizen may be told. Imports no transport library.
//	consentrequest/  domain core — asking the citizen for consent: pushed
//	                 request, code exchange, the consent token in the session
//	outway/          driven adapter — the source and the consent portal,
//	                 through the FSC Outway
//	sessionstore/    driven adapter — sessions, in memory
//	logbook/         driven adapter — the consumer's Logboek Dataverwerkingen
//	devportal/       driven adapter — best-effort dev-portal timeline
//	consumerhttp/    driving adapter — the HTTP API, its trace middleware and
//	                 the consent routes
//	main.go          configuration, wiring and process lifecycle, nothing else
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gbo-demo/dienstverlener-backend/consentrequest"
	"gbo-demo/dienstverlener-backend/consumer"
	"gbo-demo/dienstverlener-backend/consumerhttp"
	"gbo-demo/dienstverlener-backend/devportal"
	"gbo-demo/dienstverlener-backend/logbook"
	"gbo-demo/dienstverlener-backend/outway"
	"gbo-demo/dienstverlener-backend/sessionstore"
	ldv "gbo-demo/ldv-client"
	"github.com/google/uuid"
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

// upstreamRequestTimeout bounds one call through the Outway.
const upstreamRequestTimeout = 30 * time.Second

// ldvDeliveryInterval is how often the LDV spool is drained. Records are
// durable the moment they are written, so this governs only how quickly they
// reach the logbook, not whether they do.
const ldvDeliveryInterval = 2 * time.Second

// sessionLifetime is how long a browser session, and the consent token it
// holds, is kept.
const sessionLifetime = 12 * time.Hour

type config struct {
	Port             string
	OrgSector        string
	DevPortalBackend string
	OutwayURL        string
	OutwayPath       string
	// Kind is what this consumer asks its source (QUERY_KIND): "bd" for
	// Hypotheek-BV's income data, "lvg" for the Installatie Register's
	// ownership check.
	Kind consumer.Kind
	// The consent request; without CONSENT_CLIENT_ID its routes are off.
	ConsentClientID         string
	ConsentRedirectURI      string
	ConsentPortalURL        string
	ConsentAuthorizationURL string
}

func loadConfig() (config, error) {
	kind, err := consumer.LookupKind(getEnv("QUERY_KIND", "bd"))
	if err != nil {
		return config{}, err
	}
	cfg := config{
		Kind:             kind,
		Port:             getEnv("PORT", "4006"),
		OrgSector:        getEnv("ORG_SECTOR", "hypotheekverlener"),
		DevPortalBackend: getEnv("DEV_PORTAL_BACKEND_URL", ""),
		OutwayURL:        getEnv("OUTWAY_URL", "http://hv-outway:8080"),
		OutwayPath:       getEnv("OUTWAY_PATH", "/bri/graphql"),

		ConsentClientID:         os.Getenv("CONSENT_CLIENT_ID"),
		ConsentRedirectURI:      os.Getenv("CONSENT_REDIRECT_URI"),
		ConsentPortalURL:        os.Getenv("CONSENT_PORTAL_URL"),
		ConsentAuthorizationURL: os.Getenv("CONSENT_AUTHORIZATION_URL"),
	}
	if cfg.ConsentClientID == "" {
		return cfg, nil
	}
	// The client id is part of the session cookie's name; a character a
	// cookie name cannot hold would make the browser never get the cookie.
	if !isPeerID(cfg.ConsentClientID) {
		return config{}, fmt.Errorf("CONSENT_CLIENT_ID: %q is not a Peer ID (digits only)", cfg.ConsentClientID)
	}
	for name, value := range map[string]string{
		"CONSENT_REDIRECT_URI":      cfg.ConsentRedirectURI,
		"CONSENT_PORTAL_URL":        cfg.ConsentPortalURL,
		"CONSENT_AUTHORIZATION_URL": cfg.ConsentAuthorizationURL,
	} {
		if u, err := url.Parse(value); err != nil || !u.IsAbs() || u.Host == "" {
			return config{}, fmt.Errorf("%s must be an absolute URL when CONSENT_CLIENT_ID is set", name)
		}
	}
	return cfg, nil
}

// isPeerID reports whether s is an FSC Peer ID: an OIN, all digits.
func isPeerID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// consentFlow wires the consent request, or returns nil when it is not
// configured.
func consentFlow(cfg config) *consumerhttp.ConsentFlow {
	if cfg.ConsentClientID == "" {
		return nil
	}
	redirect, _ := url.Parse(cfg.ConsentRedirectURI)
	return &consumerhttp.ConsentFlow{
		Requester: &consentrequest.Requester{
			ClientID:    cfg.ConsentClientID,
			RedirectURI: cfg.ConsentRedirectURI,
			PortalURL:   cfg.ConsentPortalURL,
			Portal: &outway.ConsentPortal{
				URL:    cfg.ConsentAuthorizationURL,
				Client: &http.Client{Timeout: upstreamRequestTimeout},
			},
			Sessions: &sessionstore.Memory{Lifetime: sessionLifetime},
		},
		CookieName: "consent_session_" + cfg.ConsentClientID,
		Secure:     redirect.Scheme == "https",
		MaxAge:     sessionLifetime,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func setupTracing(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	res, _ := resource.New(ctx, resource.WithAttributes(semconv.ServiceName(serviceName)))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp, sdktrace.WithBatchTimeout(100*time.Millisecond)),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown, nil
}

func main() {
	serviceName := getEnv("OTEL_SERVICE_NAME", "dienstverlener-backend")
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}).
		WithAttrs([]slog.Attr{slog.String("service", serviceName)})))
	cfg, err := loadConfig()
	if err != nil {
		fatal("loading configuration from environment", err)
	}

	ctx := context.Background()
	shutdown, err := setupTracing(ctx, serviceName)
	if err != nil {
		slog.Error("otel setup failed", "err", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(shutCtx)
	}()

	c := &consumer.Consumer{
		Kind: cfg.Kind,
		Source: &outway.Source{
			URL:    cfg.OutwayURL + cfg.OutwayPath,
			Client: &http.Client{Timeout: upstreamRequestTimeout},
		},
	}

	// The consumer's own logbook. Without LDV_LOGBOOK_URL it writes no
	// records; with one, every call to a source is made durable in a local
	// spool before its answer is used, and delivered afterwards.
	logbookClient, err := ldv.New(ldv.Config{
		ServiceName: serviceName,
		LogbookURL:  os.Getenv("LDV_LOGBOOK_URL"),
		WriteToken:  os.Getenv("LDV_WRITE_TOKEN"),
	})
	if err != nil {
		fatal("configuring the logboek client", err)
	}
	if logbookClient != nil {
		outbox, err := ldv.OpenOutbox(getEnv("LDV_OUTBOX_PATH", "/data/ldv-outbox.jsonl"), logbookClient)
		if err != nil {
			fatal("opening the LDV outbox", err)
		}
		defer func() { _ = outbox.Close() }()
		logbookClient.UseOutbox(outbox)
		go outbox.Run(ctx, ldvDeliveryInterval)
		c.Logbook = &logbook.Logbook{
			Client:       logbookClient,
			NextLogbooks: logbook.ParseNextLogbooks(os.Getenv("LDV_NEXT_LOGBOOK_IDS")),
		}
	}

	if cfg.DevPortalBackend != "" {
		c.History = &devportal.History{Base: cfg.DevPortalBackend}
	}

	flow := consentFlow(cfg)
	if flow == nil {
		slog.Info("consent request routes off: CONSENT_CLIENT_ID is not set")
	}
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           consumerhttp.NewHandler(c, flow, serviceName),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	slog.Info("starting",
		"addr", srv.Addr, "outway", cfg.OutwayURL+cfg.OutwayPath, "sector", cfg.OrgSector,
		"req_id", uuid.New().String())
	serve(srv)
}

// fatal logs and ends the process. main is the only place in this service
// that exits; everything else returns an error.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err.Error())
	os.Exit(1)
}

// serve runs the server until the process is asked to stop, then drains it.
// Without this a SIGTERM (docker compose down, a Kubernetes rollout) killed
// in-flight requests outright. Previously a failed ListenAndServe was only
// logged, so a service that could not bind its port exited 0.
func serve(srv *http.Server) {
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
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(drainCtx); err != nil {
		slog.Warn("drain did not finish; closing remaining connections", "err", err.Error())
		_ = srv.Close()
	}
}
