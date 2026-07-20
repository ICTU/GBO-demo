package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
)

type ScopeEntry struct {
	Bronhouder      string   `json:"bronhouder"`
	ScopeID         string   `json:"scope_id"`
	ConsentedFields []string `json:"consented_fields"`
}

// Consent identifies its subject by PI (polymorphic identifier from BSNk).
// Plain BSN never enters the consent register: pseudonymisation happens before
// consent creation, and the PEP resolves PI→BSN only after a positive PDP
// decision, inside the PEP boundary.
type Consent struct {
	ConsentID        string       `json:"consent_id"`
	Status           string       `json:"status"`
	PI               string       `json:"pi"`
	DienstverlenrOIN string       `json:"dienstverlener_oin"`
	Scopes           []string     `json:"scopes"`
	ScopeEntries     []ScopeEntry `json:"scope_entries,omitempty"`
	UseCase          string       `json:"use_case"`
	CreatedAt        time.Time    `json:"created_at"`
	ValidUntil       time.Time    `json:"valid_until"`
}

// defaultValiditySeconds is the consent lifetime when the request does not
// specify one. validity_seconds (a duration) is converted once at creation
// into valid_until (an absolute timestamp); the chain only uses valid_until.
const defaultValiditySeconds = 365 * 24 * 60 * 60 // 1 year

type Store struct {
	mu       sync.RWMutex
	consents map[string]*Consent
}

func NewStore() *Store {
	return &Store{consents: make(map[string]*Consent)}
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
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
		serviceName = "consent-register"
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

	mux.HandleFunc("/consents", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		switch r.Method {
		case http.MethodPost:
			var req struct {
				PI               string       `json:"pi"`
				DienstverlenrOIN string       `json:"dienstverlener_oin"`
				Scopes           []string     `json:"scopes"`
				ScopeEntries     []ScopeEntry `json:"scope_entries"`
				UseCase          string       `json:"use_case"`
				ValiditySeconds  int          `json:"validity_seconds"` // optional; consent lifetime
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
				return
			}
			if req.PI == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "pi is required (no plain BSN accepted)"})
				return
			}
			validity := req.ValiditySeconds
			if validity <= 0 {
				validity = defaultValiditySeconds
			}
			// When scope_entries provided, derive flat scopes list for backward compat
			scopes := req.Scopes
			if len(req.ScopeEntries) > 0 {
				seen := make(map[string]bool)
				scopes = nil
				for _, se := range req.ScopeEntries {
					if !seen[se.ScopeID] {
						seen[se.ScopeID] = true
						scopes = append(scopes, se.ScopeID)
					}
				}
			}
			now := time.Now().UTC()
			c := &Consent{
				ConsentID:        "c-" + uuid.New().String(),
				Status:           "ACTIVE",
				PI:               req.PI,
				DienstverlenrOIN: req.DienstverlenrOIN,
				Scopes:           scopes,
				ScopeEntries:     req.ScopeEntries,
				UseCase:          req.UseCase,
				CreatedAt:        now,
				ValidUntil:       now.Add(time.Duration(validity) * time.Second),
			}
			store.mu.Lock()
			store.consents[c.ConsentID] = c
			store.mu.Unlock()
			writeJSON(w, http.StatusCreated, c)

		case http.MethodGet:
			// GET /consents?pi=<pi>&scope=<scope>&status=<status>
			// pi + scope: filter for the policy lookup (which consent
			// covers this subject+scope combination).
			pi := r.URL.Query().Get("pi")
			scope := r.URL.Query().Get("scope")
			statusFilter := r.URL.Query().Get("status")
			store.mu.RLock()
			var result []*Consent
			for _, c := range store.consents {
				if pi != "" && c.PI != pi {
					continue
				}
				if statusFilter != "" && c.Status != statusFilter {
					continue
				}
				if scope != "" {
					has := false
					for _, s := range c.Scopes {
						if s == scope {
							has = true
							break
						}
					}
					if !has {
						continue
					}
				}
				result = append(result, c)
			}
			store.mu.RUnlock()
			if result == nil {
				result = []*Consent{}
			}
			writeJSON(w, http.StatusOK, result)

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})

	mux.HandleFunc("/consents/", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		id := strings.TrimPrefix(r.URL.Path, "/consents/")
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing consent_id"})
			return
		}

		switch r.Method {
		case http.MethodGet:
			store.mu.RLock()
			c, ok := store.consents[id]
			store.mu.RUnlock()
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "consent not found"})
				return
			}
			writeJSON(w, http.StatusOK, c)

		case http.MethodDelete:
			store.mu.Lock()
			c, ok := store.consents[id]
			if ok {
				c.Status = "REVOKED"
			}
			store.mu.Unlock()
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "consent not found"})
				return
			}
			writeJSON(w, http.StatusOK, c)

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})

	return mux
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "consent-register"))

	shutdown := initTracer()
	defer func() { _ = shutdown(context.Background()) }()

	store := NewStore()
	mux := newMux(store)

	addr := ":4002"
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, otelhttp.NewHandler(withAccessLog(mux), "consent-register")); err != nil {
		slog.Error("server error", "err", err.Error())
		os.Exit(1)
	}
}
