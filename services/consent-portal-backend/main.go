// Package main implements the citizen-facing consent portal backend as a
// separate component. It owns the BSN boundary on the citizen side: the
// citizen-facing frontend (toestemmingsportaal-frontend :9002) talks to
// this service over a token, not by sending a plain BSN as a JSON field.
// The portal performs the BSNk pseudonymisation and registers the consent
// in the consent register with PI as subject — the register never sees a
// plain BSN.
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
	"sync"
	"time"

	"net/url"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// jwtSecret signs mock-DigiD tokens. In this demo the portal is both the
// issuer and the verifier (inline JWT); a future slice can split that out
// into a dedicated mock-DigiD service with a JWKS endpoint.
const jwtSecret = "gbo-demo-portal-secret-do-not-use-in-production"

// portalOIN is the portal's OIN used as recipient_oin when calling BSNk for
// the ownership-check step at revoke time. The PI returned by BSNk is
// deterministic per BSN regardless of recipient_oin, so any value works
// here; using the portal's own OIN is the clearest narrative.
const portalOIN = "00000000000000000002" // mock-portal OIN

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

// ── DigiD token ───────────────────────────────────────────────────────────

// PortalClaims is the mock-DigiD JWT payload. sub carries the BSN; in a real
// system the BSN would never appear in a bearer claim - DigiD returns an
// identifier from which BSN is later resolved at the service. For the demo
// the simplification is acceptable because the portal IS the resolver.
type PortalClaims struct {
	BSN string `json:"sub"`
	jwt.RegisteredClaims
}

func signPortalToken(bsn string) (string, error) {
	now := time.Now()
	claims := PortalClaims{
		BSN: bsn,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "mock-digid",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
}

func parseBearerToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", fmt.Errorf("invalid Authorization header")
	}
	return parts[1], nil
}

func validatePortalToken(tokenStr string) (string, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &PortalClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(jwtSecret), nil
	})
	if err != nil {
		return "", err
	}
	claims, ok := token.Claims.(*PortalClaims)
	if !ok || !token.Valid {
		return "", fmt.Errorf("invalid token")
	}
	if claims.BSN == "" {
		return "", fmt.Errorf("token missing sub")
	}
	return claims.BSN, nil
}

// ── SSE hub ───────────────────────────────────────────────────────────────

// SSEEvent is the portal-side stream shape (step + component + data).
// step names are portal-specific (pseudonymizing, consent_granted, etc.).
type SSEEvent struct {
	Step      string          `json:"step"`
	Component string          `json:"component,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type SSEHub struct {
	mu      sync.Mutex
	clients map[string]chan SSEEvent
}

func NewSSEHub() *SSEHub {
	return &SSEHub{clients: make(map[string]chan SSEEvent)}
}

func (h *SSEHub) Subscribe() (string, chan SSEEvent) {
	id := uuid.New().String()
	ch := make(chan SSEEvent, 32)
	h.mu.Lock()
	h.clients[id] = ch
	h.mu.Unlock()
	return id, ch
}

func (h *SSEHub) Unsubscribe(id string) {
	h.mu.Lock()
	if ch, ok := h.clients[id]; ok {
		close(ch)
		delete(h.clients, id)
	}
	h.mu.Unlock()
}

func (h *SSEHub) Broadcast(evt SSEEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.clients {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (h *SSEHub) emit(step, component string, data any) {
	raw, _ := json.Marshal(data)
	h.Broadcast(SSEEvent{Step: step, Component: component, Data: json.RawMessage(raw)})
}

// ── HTTP helpers ──────────────────────────────────────────────────────────

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("json encode failed", "err", err.Error())
	}
}

// APICall is the shape the dev-portal renders as upstream-call cards.
type APICall struct {
	ID           string          `json:"id"`
	Label        string          `json:"label"`
	Method       string          `json:"method"`
	URL          string          `json:"url"`
	Status       int             `json:"status"`
	RequestBody  json.RawMessage `json:"request_body,omitempty"`
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
	DurationMS   int64           `json:"duration_ms"`
}

func doJSONTracked(ctx context.Context, method, url string, body any, result any, headers ...map[string]string) (respBytes []byte, statusCode int, durationMS int64, err error) {
	var bodyBytes []byte
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("marshal: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, hm := range headers {
		for k, v := range hm {
			req.Header.Set(k, v)
		}
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	durationMS = time.Since(start).Milliseconds()
	if err != nil {
		return nil, 0, durationMS, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	statusCode = resp.StatusCode
	respBytes, _ = io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return respBytes, statusCode, durationMS, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(respBytes))
	}
	if result != nil {
		if err := json.Unmarshal(respBytes, result); err != nil {
			return respBytes, statusCode, durationMS, fmt.Errorf("decode: %w", err)
		}
	}
	return respBytes, statusCode, durationMS, nil
}

// ── Handlers ──────────────────────────────────────────────────────────────

type ScopeEntry struct {
	Bronhouder      string   `json:"bronhouder"`
	ScopeID         string   `json:"scope_id"`
	ConsentedFields []string `json:"consented_fields"`
}

type LoginRequest struct {
	CitizenBSN string `json:"citizen_bsn"`
}

type LoginResponse struct {
	Token string `json:"token"`
}

type GiveConsentRequest struct {
	DienstverlenrOIN string       `json:"dienstverlener_oin"`
	Scopes           []string     `json:"scopes"`
	ScopeEntries     []ScopeEntry `json:"scope_entries"`
	ValiditySeconds  int          `json:"validity_seconds,omitempty"`
}

type GiveConsentResponse struct {
	ConsentID string    `json:"consent_id"`
	Pseudonym string    `json:"pseudonym"`
	PI        string    `json:"pi"`
	TraceID   string    `json:"trace_id"`
	APICalls  []APICall `json:"api_calls"`
}

// handleLogin mocks the DigiD interaction. A real DigiD flow would never
// take the BSN as a JSON field - it would identify the citizen via SAML/OIDC
// and the BSN would surface only inside the portal's session. For demo
// purposes the mock collapses identification into the BSN field and signs a
// bearer token; the boundary that matters is that this token (not a raw BSN)
// is what crosses the frontend <-> portal interface afterwards.
func handleLogin() http.HandlerFunc {
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
		var req LoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CitizenBSN == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "citizen_bsn is required"})
			return
		}
		token, err := signPortalToken(req.CitizenBSN)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, LoginResponse{Token: token})
	}
}

// piForBSN asks BSNk for the deterministic PI of this BSN. The pseudonym
// field varies per recipient_oin but the PI is constant; we use it as an
// ownership anchor when revoking.
func piForBSN(ctx context.Context, bsnkURL, bsn string) (string, error) {
	var resp struct {
		Pseudonym string `json:"pseudonym"`
		PI        string `json:"pi"`
	}
	_, _, _, err := doJSONTracked(ctx, http.MethodPost, bsnkURL+"/pseudonymize",
		map[string]any{"bsn": bsn, "recipient_oin": portalOIN}, &resp)
	if err != nil {
		return "", err
	}
	return resp.PI, nil
}

func handleGiveConsent(cfg config, hub *SSEHub) http.HandlerFunc {
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

		tokenStr, err := parseBearerToken(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		bsn, err := validatePortalToken(tokenStr)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token: " + err.Error()})
			return
		}

		var req GiveConsentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		tracer := otel.Tracer("consent-portal-backend")
		ctx, span := tracer.Start(r.Context(), "portal.give_consent")
		defer span.End()
		traceID := span.SpanContext().TraceID().String()

		var apiCalls []APICall

		// Light up the portal component first - the citizen-side flow
		// arrives here before it fans out to BSNk and the consent register.
		hub.emit("portal_received", "toestemmingsportaal", map[string]any{"oin": req.DienstverlenrOIN})

		// Step 1: pseudonymise BSN -> PI via BSNk. recipient_oin is the
		// dienstverlener that will receive the pseudonym; PI is the
		// (recipient-independent) subject for the consent register.
		hub.emit("pseudonymizing", "bsnk-mock", map[string]any{"oin": req.DienstverlenrOIN})
		pseudoReqBody := map[string]any{"bsn": bsn, "recipient_oin": req.DienstverlenrOIN}
		var pseudoResp struct {
			Pseudonym string `json:"pseudonym"`
			PI        string `json:"pi"`
		}
		pseudoURL := cfg.BSNkURL + "/pseudonymize"
		pseudoBytes, pseudoStatus, pseudoDur, err := doJSONTracked(ctx, http.MethodPost, pseudoURL, pseudoReqBody, &pseudoResp)
		pseudoReqRaw, _ := json.Marshal(pseudoReqBody)
		pseudoCall := APICall{
			ID:          uuid.New().String(),
			Label:       "Pseudonymize BSN",
			Method:      "POST",
			URL:         pseudoURL,
			Status:      pseudoStatus,
			RequestBody: json.RawMessage(pseudoReqRaw),
			DurationMS:  pseudoDur,
		}
		if len(pseudoBytes) > 0 {
			pseudoCall.ResponseBody = json.RawMessage(pseudoBytes)
		}
		if err != nil {
			if pseudoStatus == 0 {
				pseudoCall.Status = 502
			}
			apiCalls = append(apiCalls, pseudoCall)
			loggerFromCtx(ctx).Error("bsnk pseudonymize failed", "err", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "api_calls": apiCalls})
			return
		}
		apiCalls = append(apiCalls, pseudoCall)
		hub.emit("pseudonym_generated", "bsnk-mock", map[string]any{"pseudonym": pseudoResp.Pseudonym, "pi": pseudoResp.PI})

		// Step 2: register consent in the consent register with PI as
		// subject. BSN does not cross this boundary.
		hub.emit("consent_granting", "consent-register", map[string]any{"pi": pseudoResp.PI, "oin": req.DienstverlenrOIN})
		consentReqBody := map[string]any{
			"pi":                 pseudoResp.PI,
			"dienstverlener_oin": req.DienstverlenrOIN,
			"scopes":             req.Scopes,
			"scope_entries":      req.ScopeEntries,
			"use_case":           "hypotheek",
			"validity_seconds":   req.ValiditySeconds,
		}
		var consentResp struct {
			ConsentID string `json:"consent_id"`
		}
		consentURL := cfg.ConsentURL + "/consents"
		consentBytes, consentStatus, consentDur, err := doJSONTracked(ctx, http.MethodPost, consentURL, consentReqBody, &consentResp)
		consentReqRaw, _ := json.Marshal(consentReqBody)
		consentCall := APICall{
			ID:          uuid.New().String(),
			Label:       "Create Consent",
			Method:      "POST",
			URL:         consentURL,
			Status:      consentStatus,
			RequestBody: json.RawMessage(consentReqRaw),
			DurationMS:  consentDur,
		}
		if len(consentBytes) > 0 {
			consentCall.ResponseBody = json.RawMessage(consentBytes)
		}
		if err != nil {
			if consentStatus == 0 {
				consentCall.Status = 502
			}
			apiCalls = append(apiCalls, consentCall)
			loggerFromCtx(ctx).Error("consent register call failed", "err", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "api_calls": apiCalls})
			return
		}
		apiCalls = append(apiCalls, consentCall)
		hub.emit("consent_granted", "consent-register", map[string]any{"consent_id": consentResp.ConsentID})
		hub.emit("flow_complete", "", map[string]any{"trace_id": traceID})

		giveResp := GiveConsentResponse{
			ConsentID: consentResp.ConsentID,
			Pseudonym: pseudoResp.Pseudonym,
			PI:        pseudoResp.PI,
			TraceID:   traceID,
			APICalls:  apiCalls,
		}
		writeJSON(w, http.StatusOK, giveResp)

		// Best-effort: log the citizen flow to dev-portal-backend history so
		// it shows up in the dev-portal alongside developer-triggered runs.
		// Skip when the dev-portal itself is the trigger — its frontend
		// already logs the run (X-Demo-Source header is the signal).
		if cfg.DevPortalBackend != "" && r.Header.Get("X-Demo-Source") != "dev-portal" {
			go postBurgerHistory(cfg.DevPortalBackend, bsn, req, giveResp, traceID)
		}
	}
}

func postBurgerHistory(devURL, bsn string, req GiveConsentRequest, giveResp GiveConsentResponse, traceID string) {
	entry := map[string]any{
		"scenario_name": fmt.Sprintf("Citizen · BSN %s", bsn),
		"tab":           "issuance",
		"payload": map[string]any{
			"citizen_bsn":        bsn,
			"dienstverlener_oin": req.DienstverlenrOIN,
			"scopes":             req.Scopes,
			"validity_seconds":   req.ValiditySeconds,
		},
		"trace_id":   traceID,
		"outcome":    "allow",
		"consent_id": giveResp.ConsentID,
		"response":   giveResp,
	}
	body, _ := json.Marshal(entry)
	httpReq, err := http.NewRequest(http.MethodPost, devURL+"/history", bytes.NewReader(body))
	if err != nil {
		slog.Warn("dev-portal-backend history post: build failed", "err", err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 3 * time.Second}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		slog.Warn("dev-portal-backend history post: unreachable", "err", err.Error())
		return
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode >= 300 {
		slog.Warn("dev-portal-backend history post: bad status", "status", httpResp.StatusCode)
	}
}

// handleListConsents serves GET /portal/consents — returns the calling
// citizen's consents by deriving their PI from the bearer-token-bound BSN.
// The register stores PI only; we filter on PI to enforce per-citizen
// isolation. effective_status is computed here so the UI can render
// active/expired/revoked without re-deriving date math.
func handleListConsents(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)

		tokenStr, err := parseBearerToken(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		bsn, err := validatePortalToken(tokenStr)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token: " + err.Error()})
			return
		}

		tracer := otel.Tracer("consent-portal-backend")
		ctx, span := tracer.Start(r.Context(), "portal.list_consents")
		defer span.End()

		callerPI, err := piForBSN(ctx, cfg.BSNkURL, bsn)
		if err != nil {
			loggerFromCtx(ctx).Error("bsnk lookup failed", "err", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}

		listURL := cfg.ConsentURL + "/consents?pi=" + url.QueryEscape(callerPI)
		var records []map[string]any // pass-through Consent records from the register
		_, _, _, err = doJSONTracked(ctx, http.MethodGet, listURL, nil, &records)
		if err != nil {
			loggerFromCtx(ctx).Error("list consents failed", "err", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}

		now := time.Now()
		for _, rec := range records {
			status, _ := rec["status"].(string)
			effective := "active"
			switch {
			case status == "REVOKED":
				effective = "revoked"
			default:
				if vu, ok := rec["valid_until"].(string); ok {
					if t, err := time.Parse(time.RFC3339Nano, vu); err == nil && t.Before(now) {
						effective = "expired"
					} else if t, err := time.Parse(time.RFC3339, vu); err == nil && t.Before(now) {
						effective = "expired"
					}
				}
			}
			rec["effective_status"] = effective
		}

		writeJSON(w, http.StatusOK, records)
	}
}

func handleRevoke(cfg config, hub *SSEHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodDelete {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		tokenStr, err := parseBearerToken(r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
		bsn, err := validatePortalToken(tokenStr)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token: " + err.Error()})
			return
		}

		consentID := strings.TrimPrefix(r.URL.Path, "/portal/consents/")
		if consentID == "" || strings.Contains(consentID, "/") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "consent_id missing"})
			return
		}

		tracer := otel.Tracer("consent-portal-backend")
		ctx, span := tracer.Start(r.Context(), "portal.revoke_consent")
		defer span.End()
		traceID := span.SpanContext().TraceID().String()

		// Same as on give-consent: surface the portal step in the
		// architecture panel before the BSNk and consent-register calls.
		hub.emit("portal_received", "toestemmingsportaal", map[string]any{"consent_id": consentID})

		// Ownership check: derive PI from token-bound BSN and verify the
		// consent record belongs to this citizen. Without this any token
		// holder could revoke any consent_id they guess.
		callerPI, err := piForBSN(ctx, cfg.BSNkURL, bsn)
		if err != nil {
			loggerFromCtx(ctx).Error("bsnk lookup failed", "err", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}

		var consentRec struct {
			PI     string `json:"pi"`
			Status string `json:"status"`
		}
		_, fetchStatus, _, err := doJSONTracked(ctx, http.MethodGet, cfg.ConsentURL+"/consents/"+consentID, nil, &consentRec)
		if err != nil {
			if fetchStatus == http.StatusNotFound {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "consent not found"})
				return
			}
			loggerFromCtx(ctx).Error("consent fetch failed", "err", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		if consentRec.PI != callerPI {
			loggerFromCtx(ctx).Info("revoke denied",
				"reason", "consent_not_owned_by_caller",
				"consent_id", consentID,
			)
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":  "consent does not belong to authenticated citizen",
				"reason": "consent_not_owned_by_caller",
			})
			return
		}

		hub.emit("consent_revoking", "consent-register", map[string]any{"consent_id": consentID})
		_, _, _, err = doJSONTracked(ctx, http.MethodDelete, cfg.ConsentURL+"/consents/"+consentID, nil, nil)
		if err != nil {
			loggerFromCtx(ctx).Error("revoke failed", "consent_id", consentID, "err", err.Error())
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		hub.emit("consent_revoked", "consent-register", map[string]any{"consent_id": consentID})
		hub.emit("flow_complete", "", map[string]any{"trace_id": traceID})

		writeJSON(w, http.StatusOK, map[string]string{"status": "REVOKED"})
	}
}

func handleSSE(hub *SSEHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		id, ch := hub.Subscribe()
		defer hub.Unsubscribe(id)

		fmt.Fprintf(w, "data: %s\n\n", `{"step":"connected"}`)
		flusher.Flush()

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case evt, open := <-ch:
				if !open {
					return
				}
				b, _ := json.Marshal(evt)
				fmt.Fprintf(w, "data: %s\n\n", string(b))
				flusher.Flush()
			case <-ticker.C:
				fmt.Fprintf(w, ": ping\n\n")
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
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

// silence unused-import warning if trace pkg unused outside loggerFromCtx
var _ = trace.SpanContextFromContext

// ── Main ──────────────────────────────────────────────────────────────────

// newMux builds the routing tree with the given config and SSE hub.
// Extracted from main so integration tests can wire the handlers to an
// httptest.Server without starting the real listener.
func newMux(cfg config, hub *SSEHub) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/portal/login", handleLogin())
	// /portal/consents dispatches on method: POST = new consent,
	// GET = list of the caller's own consents. DELETE /portal/consents/{id} below.
	giveH := handleGiveConsent(cfg, hub)
	listH := handleListConsents(cfg)
	mux.HandleFunc("/portal/consents", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			giveH(w, r)
		case http.MethodGet:
			listH(w, r)
		case http.MethodOptions:
			corsHeaders(w)
			w.WriteHeader(http.StatusNoContent)
		default:
			corsHeaders(w)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})
	mux.HandleFunc("/portal/consents/", handleRevoke(cfg, hub))
	mux.HandleFunc("/portal/events", handleSSE(hub))

	return mux
}

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
