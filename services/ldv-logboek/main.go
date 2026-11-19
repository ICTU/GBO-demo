// Command ldv-logboek is the Logboek Dataverwerkingen of one Verantwoordelijke.
//
// One image, one instance per Verantwoordelijke — the same pattern as the
// sidecars. LDV is explicit that each Verantwoordelijke logs its own
// processing and that only trace metadata crosses a boundary, so a shared
// logbook would be the wrong shape however convenient it looks in a demo.
// Which organisation an instance belongs to comes from its register document
// and its environment; the code knows nothing about Belastingdienst.
//
// With LDV_PROXY_UPSTREAM set, the same image is also the reference
// component of #689: a proxy in the source's request chain that records each
// delivery under a consent in this logbook and registers its trace at the
// consent usage index (internal/proxy).
//
// This file is the composition root: configuration, construction, lifecycle.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ldv-logboek/internal/httpapi"
	"ldv-logboek/internal/ldv"
	"ldv-logboek/internal/proxy"
	"ldv-logboek/internal/sqlite"
	"ldv-logboek/internal/usageindex"

	ldvclient "gbo-demo/ldv-client"

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
	Port         string
	DatabasePath string
	RegisterPath string
	WriteToken   string
	ReadToken    string
	// TLSCertPath and TLSKeyPath enable TLS. §3.2.1: "Het Logboek MOET TLS
	// kunnen afdwingen" — the capability is required, using it is not, which
	// is why these are optional and why a deployment behind a terminating
	// proxy leaves them empty.
	TLSCertPath string
	TLSKeyPath  string
	// LogbookID is the URI of this logbook's read API — the value a record's
	// dpl.read.nextLogbookId points at, and what the read response names
	// itself by. Defaults to the register's base host, which is right for the
	// demo and wrong for anything else.
	LogbookID string

	// Proxy mode (#689). ProxyUpstream switches it on: the address of what
	// the component sits in front of, the source's sidecar.
	ProxyUpstream string
	ProxyPort     string
	// ProxyActivity is the processing activity a delivery is recorded
	// under, one per source, from this logbook's register.
	ProxyActivity string
	// UsageIndexURL is the source peer's Outway grant link for the consent
	// usage index, and UsageLogbookID the logbook ID a registration names:
	// the peer ID plus the name of this component's read service.
	UsageIndexURL  string
	UsageLogbookID string
}

func loadConfig() config {
	return config{
		Port:         getEnv("PORT", "4016"),
		DatabasePath: getEnv("DATABASE_PATH", "/data/logboek.db"),
		RegisterPath: getEnv("REGISTER_PATH", "/config/verwerkingsactiviteiten.json"),
		WriteToken:   os.Getenv("LDV_WRITE_TOKEN"),
		ReadToken:    os.Getenv("LDV_READ_TOKEN"),
		LogbookID:    os.Getenv("LDV_LOGBOOK_ID"),
		TLSCertPath:  os.Getenv("LDV_TLS_CERT_PATH"),
		TLSKeyPath:   os.Getenv("LDV_TLS_KEY_PATH"),

		ProxyUpstream:  os.Getenv("LDV_PROXY_UPSTREAM"),
		ProxyPort:      getEnv("LDV_PROXY_PORT", "4017"),
		ProxyActivity:  os.Getenv("LDV_PROXY_ACTIVITY"),
		UsageIndexURL:  os.Getenv("CONSENT_USAGE_INDEX_URL"),
		UsageLogbookID: os.Getenv("CONSENT_USAGE_LOGBOOK_ID"),
	}
}

// proxyMode reports whether this instance is the #689 component, and refuses
// a half-configured one: a component that cannot record or register would
// withhold every delivery, and one that skipped either would break the
// fail-closed promise.
func (c config) proxyMode() (bool, error) {
	if c.ProxyUpstream == "" {
		return false, nil
	}
	for name, value := range map[string]string{
		"LDV_PROXY_ACTIVITY":       c.ProxyActivity,
		"CONSENT_USAGE_INDEX_URL":  c.UsageIndexURL,
		"CONSENT_USAGE_LOGBOOK_ID": c.UsageLogbookID,
	} {
		if value == "" {
			return false, fmt.Errorf("%s is required with LDV_PROXY_UPSTREAM", name)
		}
	}
	return true, nil
}

// initTracer lets the SDK read OTEL_EXPORTER_OTLP_* itself (no
// WithEndpoint), so the component works under compose and under Aspire.
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

// tlsEnabled reports whether the logbook should terminate TLS itself, and
// refuses a half-configured pair.
//
// Both or neither: one without the other is a deployment that meant to serve
// TLS and will silently serve plaintext instead — records of every processing
// about a person, in the clear, because of one unset variable.
func (c config) tlsEnabled() (bool, error) {
	switch {
	case c.TLSCertPath == "" && c.TLSKeyPath == "":
		return false, nil
	case c.TLSCertPath == "" || c.TLSKeyPath == "":
		return false, fmt.Errorf("LDV_TLS_CERT_PATH and LDV_TLS_KEY_PATH must be set together")
	}
	for name, path := range map[string]string{"certificate": c.TLSCertPath, "key": c.TLSKeyPath} {
		if _, err := os.Stat(path); err != nil {
			return false, fmt.Errorf("TLS %s: %w", name, err)
		}
	}
	return true, nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func main() {
	serviceName := getEnv("OTEL_SERVICE_NAME", "ldv-logboek")
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", serviceName))
	cfg := loadConfig()

	// No token, no logbook. The write endpoint is the only way records get in;
	// starting without one would quietly turn a Verantwoordelijke's logboek
	// into a write-anything endpoint on the internal network.
	if cfg.WriteToken == "" {
		slog.Error("LDV_WRITE_TOKEN is required")
		os.Exit(1)
	}
	// Reading a logbook is a different capability from writing one: every
	// instrumented component writes, and almost nothing should read. Without
	// a read token the extensie lezen simply is not served, rather than being
	// served to whoever holds the write token.
	if cfg.ReadToken == "" {
		slog.Warn("no LDV_READ_TOKEN configured; the read extension will refuse every request")
	}

	register, err := ldv.LoadRegister(cfg.RegisterPath)
	if err != nil {
		slog.Error("loading verwerkingsactiviteiten register", "err", err)
		os.Exit(1)
	}

	repository, err := sqlite.Open(cfg.DatabasePath)
	if err != nil {
		slog.Error("opening logboek store", "err", err)
		os.Exit(1)
	}
	defer func() { _ = repository.Close() }()

	logbook, err := ldv.NewLogbook(repository, register, time.Now)
	if err != nil {
		slog.Error("wiring logbook", "err", err)
		os.Exit(1)
	}

	logbookID := cfg.LogbookID
	if logbookID == "" {
		// Derived from the register so a demo instance needs no extra
		// configuration; a deployment sets it explicitly.
		logbookID = strings.TrimSuffix(register.BaseURI, "/verwerkingsactiviteiten") + "/data-processing-operations"
		slog.Warn("no LDV_LOGBOOK_ID configured; derived one from the register", "logbook_id", logbookID)
	}

	stored, err := repository.Count(context.Background())
	if err != nil {
		slog.Error("reading logboek store", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.NewHandler(logbook, cfg.WriteToken, cfg.ReadToken, logbookID),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	inProxyMode, err := cfg.proxyMode()
	if err != nil {
		slog.Error("proxy configuration", "err", err)
		os.Exit(1)
	}
	var proxyServer *http.Server
	if inProxyMode {
		shutdownTracer := initTracer(context.Background())
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = shutdownTracer(ctx)
		}()
		client := &http.Client{
			Timeout: 15 * time.Second,
			// OTel client span outside, the LDV traceparent put back inside.
			Transport: otelhttp.NewTransport(ldvclient.Transport(http.DefaultTransport)),
		}
		recorder, err := ldv.NewDeliveryRecorder(logbook, usageindex.Client{
			URL: cfg.UsageIndexURL, LogbookID: cfg.UsageLogbookID,
			HTTP: &http.Client{Timeout: 5 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)},
		}, cfg.ProxyActivity)
		if err != nil {
			slog.Error("wiring the proxy", "err", err)
			os.Exit(1)
		}
		proxyHandler := &proxy.Handler{Upstream: cfg.ProxyUpstream, Client: client, Recorder: recorder}
		mux := http.NewServeMux()
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
		mux.Handle("/", proxyHandler)
		proxyServer = &http.Server{
			Addr:              ":" + cfg.ProxyPort,
			Handler:           otelhttp.NewHandler(mux, serviceName, otelhttp.WithFilter(filters.Not(filters.Path("/health")))),
			ReadHeaderTimeout: readHeaderTimeout,
		}
		go func() {
			slog.Info("proxy listening", "addr", proxyServer.Addr, "upstream", cfg.ProxyUpstream,
				"activity", cfg.ProxyActivity, "usage_logbook_id", cfg.UsageLogbookID)
			if err := proxyServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				slog.Error("serving proxy", "err", err)
				os.Exit(1)
			}
		}()
	}

	useTLS, err := cfg.tlsEnabled()
	if err != nil {
		slog.Error("TLS configuration", "err", err)
		os.Exit(1)
	}

	go func() {
		slog.Info("logboek listening",
			"addr", srv.Addr,
			"verantwoordelijke", register.Verantwoordelijke,
			"verwerkingsactiviteiten", len(register.URIs()),
			"records_on_disk", stored,
			"database", cfg.DatabasePath,
			"tls", useTLS,
		)
		serveErr := srv.ListenAndServe
		if useTLS {
			serveErr = func() error { return srv.ListenAndServeTLS(cfg.TLSCertPath, cfg.TLSKeyPath) }
		}
		if err := serveErr(); !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serving logboek", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	stop()

	slog.Info("shutting down")
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if proxyServer != nil {
		if err := proxyServer.Shutdown(drainCtx); err != nil {
			_ = proxyServer.Close()
		}
	}
	if err := srv.Shutdown(drainCtx); err != nil {
		slog.Warn("drain did not finish; closing remaining connections", "err", err)
		_ = srv.Close()
	}
}
