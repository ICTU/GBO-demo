// Package main implements the demo "dienstverlener-backend" — the server-side
// component of a fictive data consumer (Hypotheek-BV). It owns the FSC-client
// boundary: the browser frontend only talks to this backend, never to FSC or
// PEP directly. In a production deployment this service would hold mTLS keys
// and FSC Outway config.
//
// Endpoint: POST /api/dvtp/query  {consent_id, scope_id?, belastingjaren?}
//
//	→ FSC Outway: pick contract by grant-link, sign token, open mTLS to Inway
//	→ FSC Inway proxy: forward GraphQL query (with PI as bsn variable)
//	   ↳ PEP → OPA → BSNk Transform → graphql-server
//	→ return  {allowed, data | reason, trace_id}
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

type config struct {
	Port             string
	OrgOIN           string
	OrgSector        string
	DevPortalBackend string
	OutwayURL        string
	OutwayPath       string
	ConsentURL       string
	HTTPClient       *http.Client
}

const upstreamRequestTimeout = 30 * time.Second

func loadConfig() config {
	return config{
		Port:             getEnv("PORT", "4006"),
		OrgOIN:           getEnv("ORG_OIN", "99999999900000000300"),
		OrgSector:        getEnv("ORG_SECTOR", "hypotheekverlener"),
		DevPortalBackend: getEnv("DEV_PORTAL_BACKEND_URL", ""),
		OutwayURL:        getEnv("OUTWAY_URL", "http://hv-outway:8080"),
		OutwayPath:       getEnv("OUTWAY_PATH", "/bri/graphql"),
		ConsentURL:       getEnv("CONSENT_URL", "http://consent-register:4002"),
		HTTPClient:       &http.Client{Timeout: upstreamRequestTimeout},
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ── HTTP helpers ─────────────────────────────────────────────────────────

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ── Query handler ────────────────────────────────────────────────────────

type queryRequest struct {
	ConsentID      string   `json:"consent_id"`
	ScopeID        string   `json:"scope_id,omitempty"`
	Belastingjaren []int    `json:"belastingjaren,omitempty"`
	Fields         []string `json:"fields,omitempty"`
}

type queryResponse struct {
	Allowed bool            `json:"allowed"`
	Data    json.RawMessage `json:"data,omitempty"`
	Reason  string          `json:"reason,omitempty"`
	TraceID string          `json:"trace_id"`
}

// newFscTransactionID returns a UUID v7 used as both the FSC-transaction-id
// and the OTel-trace-id — one identifier end-to-end across the chain. The
// FSC-Outway strictly validates v7, so v4 is not accepted.
func newFscTransactionID() string {
	u, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return u.String()
}

// fetchConsentPI resolves the pseudonym (PI) for a given consent-id from the
// consent-register. The PI, not the consent-id, travels in transit inside the
// GraphQL query variable; the sidecar at the source resolves PI→BSN. The
// consent-register URL comes from config.
func fetchConsentPI(ctx context.Context, client *http.Client, consentURL, consentID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, consentURL+"/consents/"+consentID, nil)
	if err != nil {
		return "", err
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("consent-register HTTP %d", resp.StatusCode)
	}
	var c struct {
		PI     string `json:"pi"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return "", err
	}
	if c.Status != "ACTIVE" {
		return "", fmt.Errorf("consent %s status=%s (not ACTIVE)", consentID, c.Status)
	}
	if c.PI == "" {
		return "", fmt.Errorf("consent %s has no PI", consentID)
	}
	return c.PI, nil
}

// buildQuery renders the GraphQL query. The query uses `burgerservicenummer`
// as its argument, but the actual value passed in the variable is a PI. This
// matches the EUDI shape exactly. The sidecar at the source resolves PI→BSN
// (subject_id_type=pseudonym), so the source always sees a BSN. The `$bsn`
// variable name is kept explicit so the PDP AST-parser picks it up as
// input.burgerservicenummer.
//
// `fields` is an optional field-selection; empty = default set of 5 fields.
// Scenarios that want to test out-of-scope fields (e.g. inkomenUitBox2) set
// fields explicitly.
func buildQuery(jaren []int, fields []string) string {
	if len(jaren) == 0 {
		jaren = []int{2024, 2025}
	}
	if len(fields) == 0 {
		fields = []string{"belastingjaar", "verzamelinkomen", "inkomenUitBox1", "grondslag { code omschrijving }", "peilDatum"}
	}
	jarenJSON, _ := json.Marshal(jaren)
	return fmt.Sprintf(`query($bsn: String!) { inkomensgegevens(input: { burgerservicenummer: $bsn, belastingjaren: %s }) { %s } }`,
		string(jarenJSON), strings.Join(fields, " "))
}

func handleQuery(cfg config) http.HandlerFunc {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: upstreamRequestTimeout}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req queryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if req.ConsentID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "consent_id is required"})
			return
		}
		if req.ScopeID == "" {
			req.ScopeID = "bd:ib:2025"
		}

		tracer := otel.Tracer("dienstverlener-backend")
		ctx, span := tracer.Start(r.Context(), "dvtp.query")
		defer span.End()
		log := loggerFromCtx(ctx)

		// The backend talks to a real FSC-Outway.
		//
		// Step 1 — Consent-lookup: fetch the PI for this consent-id from the
		// consent-register. The PI travels in the query; the consent-id does
		// not.
		pi, err := fetchConsentPI(ctx, client, cfg.ConsentURL, req.ConsentID)
		if err != nil {
			log.Warn("consent-lookup failed", "consent_id", req.ConsentID, "err", err.Error())
			writeJSON(w, http.StatusForbidden, map[string]any{
				"allowed":  false,
				"reason":   "consent_lookup_failed: " + err.Error(),
				"trace_id": traceIDFromSpan(span),
			})
			return
		}

		// Step 2 — POST to the Outway at /bri/graphql. The body is pure
		// GraphQL, with variables.bsn = PI (the sidecar at the source
		// substitutes it back to BSN). No separate token-fetch is needed:
		// the Outway picks a contract by grant-link, signs the token
		// internally, and opens mTLS to the Inway.
		query := buildQuery(req.Belastingjaren, req.Fields)
		vars := map[string]string{"bsn": pi}
		proxyBody, _ := json.Marshal(map[string]any{
			"query":     query,
			"variables": vars,
		})
		proxyReq, _ := http.NewRequestWithContext(ctx, http.MethodPost,
			cfg.OutwayURL+cfg.OutwayPath, bytes.NewReader(proxyBody))
		proxyReq.Header.Set("Content-Type", "application/json")
		// Untrusted context-header: X-GBO-Scope carries the requested scope.
		// (There is no X-GBO-Flow header; flow is a grant-property.)
		proxyReq.Header.Set("X-GBO-Scope", req.ScopeID)
		// Fsc-Transaction-Id doubles as the OTel-trace-id — one identifier
		// end-to-end across the FSC chain. Also written as a span-attribute
		// so downstream cross-trace-lookups can find the PDP-span even when
		// the AuthZen-plugin drops the OTel context.
		fscTxID := newFscTransactionID()
		proxyReq.Header.Set("Fsc-Transaction-Id", fscTxID)
		span.SetAttributes(attribute.String("gbo.fsc.transaction_id", fscTxID))
		otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(proxyReq.Header))

		traceID := traceIDFromSpan(span)
		proxyResp, err := client.Do(proxyReq)
		if err != nil {
			log.Error("fsc outway call failed", "err", err.Error())
			writeJSON(w, http.StatusBadGateway, queryResponse{
				Allowed: false,
				Reason:  "fsc_outway_call_failed: " + err.Error(),
				TraceID: traceID,
			})
			return
		}
		defer proxyResp.Body.Close()
		proxyRespBody, _ := io.ReadAll(proxyResp.Body)

		// Skip backend-side history-post when the dev-portal is the trigger;
		// its frontend already logs the run (X-Demo-Source header signals).
		fromDevPortal := r.Header.Get("X-Demo-Source") == "dev-portal"

		// 200 = ALLOW with data, 403 = DENY with reason, other = error
		if proxyResp.StatusCode == http.StatusOK {
			log.Info("query allowed", "consent_id", req.ConsentID, "trace_id", traceID)
			resp := queryResponse{
				Allowed: true,
				Data:    json.RawMessage(proxyRespBody),
				TraceID: traceID,
			}
			writeJSON(w, http.StatusOK, resp)
			if cfg.DevPortalBackend != "" && !fromDevPortal {
				go postUseHistory(cfg.DevPortalBackend, req, resp, traceID)
			}
			return
		}

		var denyResp struct {
			Allowed bool   `json:"allowed"`
			Reason  string `json:"reason"`
		}
		_ = json.Unmarshal(proxyRespBody, &denyResp)
		if denyResp.Reason == "" {
			denyResp.Reason = fmt.Sprintf("upstream_error: status %d", proxyResp.StatusCode)
		}
		log.Info("query denied", "consent_id", req.ConsentID, "reason", denyResp.Reason, "trace_id", traceID)
		resp := queryResponse{
			Allowed: false,
			Reason:  denyResp.Reason,
			TraceID: traceID,
		}
		writeJSON(w, http.StatusOK, resp)
		if cfg.DevPortalBackend != "" && !fromDevPortal {
			go postUseHistory(cfg.DevPortalBackend, req, resp, traceID)
		}
	}
}

// Best-effort: log the use-query to dev-portal-backend history. Failures
// are silent — the afnemer-flow is the primary concern.
func postUseHistory(devURL string, req queryRequest, qResp queryResponse, traceID string) {
	outcome := "deny"
	if qResp.Allowed {
		outcome = "allow"
	}
	entry := map[string]any{
		"scenario_name": fmt.Sprintf("Afnemer · use · scope %s", req.ScopeID),
		"tab":           "use",
		"payload": map[string]any{
			"consent_id":     req.ConsentID,
			"scope_id":       req.ScopeID,
			"belastingjaren": req.Belastingjaren,
			"fields":         req.Fields,
		},
		"trace_id":   traceID,
		"outcome":    outcome,
		"consent_id": req.ConsentID,
		"response":   qResp,
	}
	body, _ := json.Marshal(entry)
	httpReq, err := http.NewRequest(http.MethodPost, devURL+"/history", bytes.NewReader(body))
	if err != nil {
		slog.Warn("dev-portal-backend history post: build failed", "err", err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		slog.Warn("dev-portal-backend history post: unreachable", "err", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("dev-portal-backend history post: bad status", "status", resp.StatusCode)
	}
}

func traceIDFromSpan(span trace.Span) string {
	sc := span.SpanContext()
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}

// ── OTel setup ───────────────────────────────────────────────────────────

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

// ── Main ─────────────────────────────────────────────────────────────────

// newMux builds the routing tree for the backend. Extracted from main so
// integration tests can wire the handlers to an httptest.Server (with
// stub OutwayURL + ConsentURL in cfg) without starting the real listener.
func newMux(cfg config) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/dvtp/query", handleQuery(cfg))
	return mux
}

func main() {
	cfg := loadConfig()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}).
		WithAttrs([]slog.Attr{slog.String("service", "dienstverlener-backend")})))

	ctx := context.Background()
	shutdown, err := setupTracing(ctx, "dienstverlener-backend")
	if err != nil {
		slog.Error("otel setup failed", "err", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(shutCtx)
	}()

	mux := newMux(cfg)
	handler := otelhttp.NewHandler(withAccessLog(mux), "dienstverlener-backend")
	addr := ":" + cfg.Port
	slog.Info("dienstverlener-backend starting",
		"addr", addr, "outway", cfg.OutwayURL+cfg.OutwayPath, "org_oin", cfg.OrgOIN, "sector", cfg.OrgSector,
		"req_id", uuid.New().String())
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("server stopped", "err", err)
	}
}
