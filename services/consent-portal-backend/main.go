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
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
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
	ldvclient "gbo-demo/ldv-client"
	"net"
	"os/signal"
	"syscall"
)

// portalOIN is the portal's own OIN. BSNk makes the polymorphic values for it
// when it activates a citizen.
const portalOIN = "00000000000000000002" // mock-portal OIN

// portalKeySetVersion is the version of the portal's own keys at BSNk.
const portalKeySetVersion = 1

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

// ldvDeliveryInterval is how often the LDV spool is drained.
const ldvDeliveryInterval = 2 * time.Second

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
	LogbookURL   string
	LogbookToken string
	// PseudonymsLogbook is where BSNk's processings can be looked up: its
	// read API, or a contact page while it has none.
	PseudonymsLogbook string
	// Sources are the parties a consent token carries an encrypted identity
	// for: each source's OIN and the version of its keys.
	Sources []consent.Party
	// SubjectRefKey is the secret the portal derives its reference to a
	// citizen with, and SubjectRefKeyVersion names it.
	SubjectRefKey        []byte
	SubjectRefKeyVersion string
}

var subjectRefKeyVersion = regexp.MustCompile(`^[0-9A-Za-z]{1,16}$`)

func loadConfig() (config, error) {
	sources, err := parseSources(os.Getenv("CONSENT_SOURCES"))
	if err != nil {
		return config{}, fmt.Errorf("CONSENT_SOURCES: %w", err)
	}
	// Without the key every reference could be recomputed from a list of
	// BSNs, so the portal does not start without one.
	key := os.Getenv("SUBJECT_REF_KEY")
	if len(key) < 32 {
		return config{}, errors.New("SUBJECT_REF_KEY: a secret of at least 32 characters is required")
	}
	version := getEnv("SUBJECT_REF_KEY_VERSION", "1")
	if !subjectRefKeyVersion.MatchString(version) {
		return config{}, fmt.Errorf("SUBJECT_REF_KEY_VERSION: %q is not 1 to 16 letters and digits", version)
	}
	return config{
		Port:              getEnv("PORT", "4005"),
		BSNkURL:           getEnv("BSNK_URL", "http://bsnk-mock:4003"),
		ConsentURL:        getEnv("CONSENT_URL", "http://consent-register:4002"),
		DevPortalBackend:  getEnv("DEV_PORTAL_BACKEND_URL", ""),
		LogbookURL:        getEnv("LDV_LOGBOOK_URL", ""),
		LogbookToken:      getEnv("LDV_WRITE_TOKEN", ""),
		PseudonymsLogbook: getEnv("LDV_BSNK_NEXT_LOGBOOK_ID", ""),
		Sources:           sources,

		SubjectRefKey:        []byte(key),
		SubjectRefKeyVersion: version,
	}, nil
}

var sourceOIN = regexp.MustCompile(`^[0-9]{20}$`)

// parseSources reads the sources a consent is given for, as a comma-separated
// list of <OIN>@<key set version>. The version is the date the source's
// certificate was issued, as YYYYMMDD. At least one source is required: a
// consent no source can read is of no use.
//
// What BSNk would refuse is refused here, at start, rather than at every
// consent: a version that is not a real date, and an OIN named twice, which a
// transformation request does not accept.
func parseSources(value string) ([]consent.Party, error) {
	var sources []consent.Party
	seen := map[string]bool{}
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		oin, version, found := strings.Cut(entry, "@")
		if !found || !sourceOIN.MatchString(oin) {
			return nil, fmt.Errorf("%q is not <OIN of 20 digits>@<key set version>", entry)
		}
		if _, err := time.Parse("20060102", version); err != nil || len(version) != 8 {
			return nil, fmt.Errorf("%q: the key set version must be a date as YYYYMMDD", entry)
		}
		keySetVersion, _ := strconv.Atoi(version)
		if seen[oin] {
			return nil, fmt.Errorf("%s is named twice; a source has one key set version at a time", oin)
		}
		seen[oin] = true
		sources = append(sources, consent.Party{OIN: oin, KeySetVersion: keySetVersion})
	}
	if len(sources) == 0 {
		return nil, errors.New("name at least one source, as <OIN>@<key set version>")
	}
	return sources, nil
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

	bsnkClient := bsnk.Client{
		Base: cfg.BSNkURL, Caller: caller,
		Requester: portalOIN, RequesterKeySetVersion: portalKeySetVersion,
	}
	return &consent.Portal{
		Identities:        bsnkClient,
		SubjectRefs:       consent.SubjectRefs{Key: cfg.SubjectRefKey, Version: cfg.SubjectRefKeyVersion},
		Consents:          register.Client{Base: cfg.ConsentURL, Caller: caller},
		Watch:             watchers,
		Logbook:           logbook,
		Sources:           cfg.Sources,
		PseudonymsLogbook: cfg.PseudonymsLogbook,
	}
}

// newLogbook builds the Logboek Dataverwerkingen adapter, or nothing when
// this deployment is not part of an LDV chain. A typed nil would satisfy the
// interface while being nil underneath, so the concrete absence is turned
// into an interface-level one here.
func newLogbook(cfg config, serviceName string) (consent.Logbook, *ldvclient.Client, error) {
	writer, client, err := ldv.New(serviceName, cfg.LogbookURL, cfg.LogbookToken)
	if err != nil || writer == nil {
		return nil, nil, err
	}
	return writer, client, nil
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

	cfg, err := loadConfig()
	if err != nil {
		fatal("reading the configuration", err)
	}
	hub := portalhttp.NewHub()

	// Either this portal is part of GBO's LDV chain and cannot start without
	// its logbook, or it is not and writes no records.
	logbook, ldvClient, err := newLogbook(cfg, serviceName)
	if err != nil {
		fatal("configuring the logboek adapter", err)
	}
	if ldvClient == nil {
		slog.Warn("no LDV_LOGBOOK_URL configured; this portal writes no Logboek Dataverwerkingen records")
	} else {
		// A local spool: the pseudonymisation record is durable before the
		// consent is created, and reaches the logbook afterwards.
		outbox, err := ldvclient.OpenOutbox(getEnv("LDV_OUTBOX_PATH", "/data/ldv-outbox.jsonl"), ldvClient)
		if err != nil {
			fatal("opening the LDV outbox", err)
		}
		defer func() { _ = outbox.Close() }()
		ldvClient.UseOutbox(outbox)
		go outbox.Run(context.Background(), ldvDeliveryInterval)
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
