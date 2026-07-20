package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

const piSalt = "pi-salt"

type Store struct {
	mu      sync.RWMutex
	piToBSN map[string]string
}

func hashHex16(input string) string {
	h := sha256.Sum256([]byte(input))
	return hex.EncodeToString(h[:])[:16]
}

func pseudonymFor(bsn, recipientOIN string) string {
	return "EP-" + hashHex16(bsn+recipientOIN)
}

func piFor(bsn string) string {
	return "PI-" + hashHex16(bsn+piSalt)
}

func NewStore() *Store {
	s := &Store{piToBSN: make(map[string]string)}
	// Pre-populate demo BSN
	demoBSN := "123456789"
	s.piToBSN[piFor(demoBSN)] = demoBSN
	return s
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

// newMux builds the routing tree with the given store. Extracted from main
// so integration tests can wire the handlers to an httptest.Server without
// starting the real listener.
func newMux(store *Store) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/pseudonymize", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			BSN          string `json:"bsn"`
			RecipientOIN string `json:"recipient_oin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if req.BSN == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bsn is required"})
			return
		}
		pseudonym := pseudonymFor(req.BSN, req.RecipientOIN)
		pi := piFor(req.BSN)

		store.mu.Lock()
		store.piToBSN[pi] = req.BSN
		store.mu.Unlock()

		writeJSON(w, http.StatusOK, map[string]string{
			"pseudonym": pseudonym,
			"pi":        pi,
		})
	})

	// /transform represents BSNk Transform: given a PI and a target
	// recipient OIN, return the recipient-specific identifier. For a
	// BSN-authorized recipient (e.g. bronhouder Belastingdienst), Transform
	// yields an Encrypted Identity (EI) that the recipient can decrypt to
	// the underlying BSN. The mock collapses Transform + decrypt into one
	// response (returns BSN directly); recipient_oin is required in the
	// request but not used by the mock — it appears in traces for
	// narrative parity with real BSNk PP.
	mux.HandleFunc("/transform", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			PI           string `json:"pi"`
			RecipientOIN string `json:"recipient_oin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if req.PI == "" || req.RecipientOIN == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pi and recipient_oin are required"})
			return
		}
		store.mu.RLock()
		bsn, ok := store.piToBSN[req.PI]
		store.mu.RUnlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("PI not found: %s", req.PI)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"bsn": bsn})
	})

	return mux
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "bsnk-mock"))

	shutdown := initTracer()
	defer func() { _ = shutdown(context.Background()) }()

	store := NewStore()
	mux := newMux(store)

	addr := ":4003"
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, otelhttp.NewHandler(withAccessLog(mux), "bsnk-mock")); err != nil {
		slog.Error("server error", "err", err.Error())
		os.Exit(1)
	}
}
