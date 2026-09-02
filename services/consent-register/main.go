package main

import (
	"context"
	"encoding/json"
	"errors"
	ldv "gbo-demo/ldv-client"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
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

// Consent stores only the consent-portal-specific subject reference. The PI is
// accepted transiently while issuing the signed consent token, but is never
// assigned to this persisted model.
type Consent struct {
	ConsentID        string       `json:"consent_id"`
	Status           string       `json:"status"`
	SubjectRef       string       `json:"subject_ref"`
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

// readHeaderTimeout bounds how long a client may take to send its request
// headers, so a stalled connection cannot hold a handler open.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds the drain after SIGTERM: stop accepting, let
// in-flight requests finish, then close whatever is left.
const shutdownTimeout = 15 * time.Second

type config struct {
	Port           string
	SigningKeyPath string
	SigningKeyID   string
	TokenIssuer    string
	TokenAudience  string
}

func loadConfig() (config, error) {
	cfg := config{
		Port:           getEnv("PORT", "4002"),
		SigningKeyPath: os.Getenv("CONSENT_SIGNING_KEY_PATH"),
		SigningKeyID:   getEnv("CONSENT_SIGNING_KEY_ID", "gbo-consent-demo-1"),
		TokenIssuer:    getEnv("CONSENT_TOKEN_ISSUER", "https://consent-register.gbo.test"),
		TokenAudience:  getEnv("CONSENT_TOKEN_AUDIENCE", "gbo:dvtp:pdp"),
	}
	if os.Getenv("DATABASE_URL") != "" && cfg.SigningKeyPath == "" {
		return config{}, errors.New("CONSENT_SIGNING_KEY_PATH is required with a persistent consent store")
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type Store struct {
	mu       sync.RWMutex
	consents map[string]*Consent
}

type ConsentFilter struct {
	SubjectRef string
	Scope      string
	Status     string
}

type ConsentStore interface {
	Create(ctx context.Context, consent *Consent) error
	List(ctx context.Context, filter ConsentFilter) ([]*Consent, error)
	Get(ctx context.Context, consentID string) (*Consent, bool, error)
	Revoke(ctx context.Context, consentID string) (*Consent, bool, error)
}

func NewStore() *Store {
	return &Store{consents: make(map[string]*Consent)}
}

func (s *Store) Create(_ context.Context, consent *Consent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.consents[consent.ConsentID] = consent

	return nil
}

func (s *Store) List(_ context.Context, filter ConsentFilter) ([]*Consent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Consent, 0)

	for _, consent := range s.consents {
		if filter.SubjectRef != "" && consent.SubjectRef != filter.SubjectRef {
			continue
		}

		if filter.Status != "" && consent.Status != filter.Status {
			continue
		}

		if filter.Scope != "" {
			hasScope := false

			for _, scope := range consent.Scopes {
				if scope == filter.Scope {
					hasScope = true

					break
				}
			}

			if !hasScope {
				continue
			}
		}

		result = append(result, consent)
	}

	return result, nil
}

func (s *Store) Get(_ context.Context, consentID string) (*Consent, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	consent, ok := s.consents[consentID]

	return consent, ok, nil
}

func (s *Store) Revoke(_ context.Context, consentID string) (*Consent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	consent, ok := s.consents[consentID]
	if !ok {
		return nil, false, nil
	}

	consent.Status = "REVOKED"

	return consent, true, nil
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
func newMux(store ConsentStore, issuer *ConsentIssuer, logbook *registerLogbook) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, issuer.JWKS())
	})

	mux.HandleFunc("/consents", handleConsents(store, issuer, logbook))

	mux.HandleFunc("/consents/", handleConsentByID(store, logbook))

	return mux
}

// handleConsents serves the collection: recording a new consent, and the
// citizen listing. Split out of newMux because a routing tree that also
// contains the handlers grows past what anyone can read at once — and past
// what gocyclo tolerates.
func handleConsents(store ConsentStore, issuer *ConsentIssuer, logbook *registerLogbook) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now().UTC()
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		switch r.Method {
		case http.MethodPost:
			var req struct {
				PI               string       `json:"pi"`
				SubjectRef       string       `json:"subject_ref"`
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
			if req.SubjectRef == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "subject_ref is required"})
				return
			}
			if req.DienstverlenrOIN == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "dienstverlener_oin is required"})
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
				SubjectRef:       req.SubjectRef,
				DienstverlenrOIN: req.DienstverlenrOIN,
				Scopes:           scopes,
				ScopeEntries:     req.ScopeEntries,
				UseCase:          req.UseCase,
				CreatedAt:        now,
				ValidUntil:       now.Add(time.Duration(validity) * time.Second),
			}
			consentToken, err := issuer.Sign(*c, req.PI)
			if err != nil {
				slog.Error("sign consent token", "consent_id", c.ConsentID, "err", err.Error())
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not issue consent token"})
				return
			}
			if err := store.Create(r.Context(), c); err != nil {
				slog.Error("create consent", "err", err.Error())
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not store consent"})
				return
			}

			// Recording a consent is itself a Dataverwerking of the GBO
			// voorziening. The record names the Betrokkene by the
			// portal-scoped reference the register stores — never the PI,
			// which exists here only inside the signed token.
			if err := logbook.logConsentOperation(r.Context(), r,
				consentGrantActivity, "dataverwerking.toestemming-verlenen",
				c.SubjectRef, start, http.StatusCreated,
				map[string]any{
					"gbo.consent.id":             c.ConsentID,
					"gbo.consent.dienstverlener": c.DienstverlenrOIN,
					"gbo.consent.scopes":         c.Scopes,
					"gbo.consent.use_case":       c.UseCase,
				}); err != nil {
				refuseUnlogged(w)
				return
			}

			writeJSON(w, http.StatusCreated, struct {
				*Consent
				ConsentToken string `json:"consent_token"`
			}{Consent: c, ConsentToken: consentToken})

		case http.MethodGet:
			// GET /consents?subject_ref=<portal-pseudonym>&scope=<scope>&status=<status>
			// subject_ref exists only for citizen-facing ownership/listing. It
			// is not an authorization input; the PDP uses the signed token.
			subjectRef := r.URL.Query().Get("subject_ref")
			scope := r.URL.Query().Get("scope")
			statusFilter := r.URL.Query().Get("status")
			result, err := store.List(r.Context(), ConsentFilter{
				SubjectRef: subjectRef,
				Scope:      scope,
				Status:     statusFilter,
			})
			if err != nil {
				slog.Error("list consents", "err", err.Error())
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list consents"})

				return
			}

			// Showing a citizen their own consents is a Dataverwerking too.
			// A listing without a subject_ref is an operational query rather
			// than inzage by a Betrokkene, and names nobody to log.
			if err := logbook.logConsentOperation(r.Context(), r,
				consentListActivity, "dataverwerking.toestemming-inzage",
				subjectRef, start, http.StatusOK,
				map[string]any{"gbo.consent.count": len(result)}); err != nil {
				refuseUnlogged(w)
				return
			}

			writeJSON(w, http.StatusOK, result)

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

// handleConsentByID serves a single consent: its status, its detail, and its
// revocation.
func handleConsentByID(store ConsentStore, logbook *registerLogbook) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now().UTC()
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

		if strings.HasSuffix(id, "/status") {
			if r.Method != http.MethodGet {
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
				return
			}
			consentID := strings.TrimSuffix(id, "/status")
			c, ok, err := store.Get(r.Context(), consentID)
			if err != nil {
				slog.Error("get consent status", "err", err.Error())
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not get consent status"})
				return
			}
			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "consent not found"})
				return
			}
			// The PDP asks this on every request. Confirming a consent's
			// status is a processing of that Betrokkene's data, and it is the
			// step that makes a revocation take effect — so it is logged like
			// any other, not treated as a read-only lookup.
			if err := logbook.logConsentOperation(r.Context(), r,
				consentStatusActivity, "dataverwerking.toestemming-status",
				c.SubjectRef, start, http.StatusOK,
				map[string]any{
					"gbo.consent.id":     c.ConsentID,
					"gbo.consent.status": c.Status,
				}); err != nil {
				refuseUnlogged(w)
				return
			}

			writeJSON(w, http.StatusOK, map[string]any{
				"consent_id": c.ConsentID,
				"status":     c.Status,
			})
			return
		}

		switch r.Method {
		case http.MethodGet:
			c, ok, err := store.Get(r.Context(), id)
			if err != nil {
				slog.Error("get consent", "err", err.Error())
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not get consent"})

				return
			}

			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "consent not found"})

				return
			}

			if err := logbook.logConsentOperation(r.Context(), r,
				consentListActivity, "dataverwerking.toestemming-inzage",
				c.SubjectRef, start, http.StatusOK,
				map[string]any{"gbo.consent.id": c.ConsentID}); err != nil {
				refuseUnlogged(w)
				return
			}

			writeJSON(w, http.StatusOK, c)

		case http.MethodDelete:
			c, ok, err := store.Revoke(r.Context(), id)
			if err != nil {
				slog.Error("revoke consent", "err", err.Error())
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not revoke consent"})

				return
			}

			if !ok {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "consent not found"})

				return
			}

			// The revocation has already been stored, so this is the one
			// place where fail-closed cannot undo the processing. It can
			// still withhold the confirmation, which is what it does: the
			// caller learns the operation did not complete cleanly rather
			// than being told all is well while the logboek has no record.
			if err := logbook.logConsentOperation(r.Context(), r,
				consentRevokeActivity, "dataverwerking.toestemming-intrekken",
				c.SubjectRef, start, http.StatusOK,
				map[string]any{
					"gbo.consent.id":     c.ConsentID,
					"gbo.consent.status": c.Status,
				}); err != nil {
				refuseUnlogged(w)
				return
			}

			writeJSON(w, http.StatusOK, c)

		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

// fatal logs and ends the process. main is the only place in this service
// that exits; everything else returns an error.
func fatal(msg string, err error) {
	slog.Error(msg, "err", err.Error())
	os.Exit(1)
}

func main() {
	serviceName := getEnv("OTEL_SERVICE_NAME", "consent-register")
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", serviceName))

	cfg, err := loadConfig()
	if err != nil {
		fatal("loading configuration from environment", err)
	}

	shutdown := initTracer()
	defer func() { _ = shutdown(context.Background()) }()

	store, closeStore, err := openConsentStore(context.Background())
	if err != nil {
		fatal("initialising consent store", err)
	}
	defer closeStore()
	issuer, err := NewConsentIssuer(cfg)
	if err != nil {
		fatal("initialising consent token issuer", err)
	}

	// Either this register is part of GBO's LDV chain and cannot start
	// without its logbook, or it is not and writes no records.
	client, err := ldv.New(ldv.Config{
		ServiceName: serviceName,
		LogbookURL:  os.Getenv("LDV_LOGBOOK_URL"),
		WriteToken:  os.Getenv("LDV_WRITE_TOKEN"),
	})
	if err != nil {
		fatal("configuring the logboek client", err)
	}
	logbook := newRegisterLogbook(client)
	if client == nil {
		slog.Warn("no LDV_LOGBOOK_URL configured; this register writes no Logboek Dataverwerkingen records")
	}

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(withAccessLog(newMux(store, issuer, logbook)), serviceName),
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
