package main

// Driving (inbound) adapters: everything that is true about HTTP and nothing
// that is true about consent. Handlers translate a request into a core call
// and a core result into a response; the domain rules live in portal.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// jwtSecret signs mock-DigiD tokens. In this demo the portal is both the
// issuer and the verifier (inline JWT); a future slice can split that out
// into a dedicated mock-DigiD service with a JWKS endpoint.
const jwtSecret = "gbo-demo-portal-secret-do-not-use-in-production"

// portalOIN is the portal's OIN, used as recipient_oin when it needs the
// caller's PI for its own sake (listing, ownership checks).
const portalOIN = "00000000000000000002" // mock-portal OIN

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

// ── Wire types ────────────────────────────────────────────────────────────

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

// ── The citizen handler seam ──────────────────────────────────────────────

// citizenFunc is what a portal endpoint actually is: an authenticated citizen
// plus the request in, a JSON body out.
type citizenFunc func(ctx context.Context, citizen BSN, r *http.Request) (any, error)

// httpError lets a handler override the status and body when the default
// mapping is not enough (give-consent attaches api_calls to its errors).
type httpError struct {
	Status int
	Body   map[string]any
}

func (e *httpError) Error() string { return fmt.Sprint(e.Body["error"]) }

// statusFor maps domain errors to status codes. This is the one legitimate
// translation between a domain outcome and a transport concern.
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrConsentNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrNotOwned):
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}

// citizen wraps one endpoint, adding — for every endpoint, automatically —
// CORS and OPTIONS, method gating, bearer parsing and JWT validation, the
// OTel span, the request-scoped observer, and error-to-status mapping.
//
// Adding a fourth portal endpoint costs a core method plus three lines here.
func (p *Portal) citizen(method, op string, fn citizenFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corsHeaders(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != method {
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

		ctx, span := otel.Tracer("consent-portal-backend").Start(r.Context(), "portal."+op)
		defer span.End()

		// The request-scoped observer is just the call log: the portal's own
		// watchers are fanned out by the core, so adding them here too would
		// deliver every event twice.
		log := &callLog{}
		ctx = withObserver(ctx, log)
		ctx = context.WithValue(ctx, callLogKey{}, log)

		res, err := fn(ctx, BSN(bsn), r)
		if err != nil {
			var he *httpError
			if errors.As(err, &he) {
				writeJSON(w, he.Status, he.Body)
				return
			}
			body := map[string]any{"error": err.Error()}
			if errors.Is(err, ErrNotOwned) {
				body["reason"] = "consent_not_owned_by_caller"
			}
			loggerFromCtx(ctx).Error("portal."+op+" failed", "err", err.Error())
			writeJSON(w, statusFor(err), body)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

type callLogKey struct{}

// callsFrom returns the upstream calls recorded so far in this request.
func callsFrom(ctx context.Context) []APICall {
	if l, ok := ctx.Value(callLogKey{}).(*callLog); ok {
		return l.snapshot()
	}
	return nil
}

func traceIDFrom(ctx context.Context) string {
	return trace.SpanContextFromContext(ctx).TraceID().String()
}

// ── Handlers ──────────────────────────────────────────────────────────────

// handleLogin mocks the DigiD interaction. A real DigiD flow would never
// take the BSN as a JSON field - it would identify the citizen via SAML/OIDC
// and the BSN would surface only inside the portal's session. For demo
// purposes the mock collapses identification into the BSN field and signs a
// bearer token; the boundary that matters is that this token (not a raw BSN)
// is what crosses the frontend <-> portal interface afterwards.
//
// It does not touch the core: signing a token from a posted BSN involves no
// domain rule, so routing it through Portal would be a pure proxy.
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

func (p *Portal) handleGiveConsent() http.HandlerFunc {
	return p.citizen(http.MethodPost, "give_consent",
		func(ctx context.Context, citizen BSN, r *http.Request) (any, error) {
			var req GiveConsentRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				return nil, &httpError{
					Status: http.StatusBadRequest,
					Body:   map[string]any{"error": "invalid request body"},
				}
			}
			in := GiveConsentInput{
				DienstverlenerOIN: req.DienstverlenrOIN,
				Scopes:            req.Scopes,
				ScopeEntries:      req.ScopeEntries,
				ValiditySeconds:   req.ValiditySeconds,
				Trigger:           r.Header.Get("X-Demo-Source"),
			}

			granted, err := p.GiveConsent(ctx, citizen, in)
			if err != nil {
				loggerFromCtx(ctx).Error("give consent failed", "err", err.Error())
				return nil, &httpError{
					Status: statusFor(err),
					Body:   map[string]any{"error": err.Error(), "api_calls": callsFrom(ctx)},
				}
			}

			traceID := traceIDFrom(ctx)
			resp := GiveConsentResponse{
				ConsentID: granted.ConsentID,
				Pseudonym: granted.Pseudonym,
				PI:        string(granted.PI),
				TraceID:   traceID,
				APICalls:  callsFrom(ctx),
			}

			// Terminal event: closes the panel narrative and hands the
			// dev-portal timeline everything it needs in one payload.
			p.observe(ctx, Event{
				Flow: "give_consent",
				Step: "flow_complete",
				Data: map[string]any{"trace_id": traceID},
				Summary: &FlowSummary{
					Citizen:           citizen,
					DienstverlenerOIN: in.DienstverlenerOIN,
					Scopes:            in.Scopes,
					ValiditySeconds:   in.ValiditySeconds,
					TraceID:           traceID,
					ConsentID:         granted.ConsentID,
					Outcome:           "allow",
					Response:          resp,
					Trigger:           in.Trigger,
				},
			})
			return resp, nil
		})
}

func (p *Portal) handleListConsents() http.HandlerFunc {
	return p.citizen(http.MethodGet, "list_consents",
		func(ctx context.Context, citizen BSN, _ *http.Request) (any, error) {
			recs, err := p.ListConsents(ctx, citizen)
			if err != nil {
				return nil, err
			}
			// Pass the register's own payload straight through, annotated
			// with the status the core computed.
			out := make([]map[string]any, 0, len(recs))
			for _, rec := range recs {
				m := rec.Raw
				if m == nil {
					m = map[string]any{"consent_id": rec.ID}
				}
				m["effective_status"] = string(rec.Effective)
				out = append(out, m)
			}
			return out, nil
		})
}

func (p *Portal) handleRevoke() http.HandlerFunc {
	return p.citizen(http.MethodDelete, "revoke_consent",
		func(ctx context.Context, citizen BSN, r *http.Request) (any, error) {
			consentID := strings.TrimPrefix(r.URL.Path, "/portal/consents/")
			if consentID == "" || strings.Contains(consentID, "/") {
				return nil, &httpError{
					Status: http.StatusBadRequest,
					Body:   map[string]any{"error": "consent_id missing"},
				}
			}
			if err := p.RevokeConsent(ctx, citizen, consentID); err != nil {
				return nil, err
			}
			p.observe(ctx, Event{
				Flow: "revoke_consent",
				Step: "flow_complete",
				Data: map[string]any{"trace_id": traceIDFrom(ctx)},
			})
			return map[string]string{"status": "REVOKED"}, nil
		})
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

// ── Routing ───────────────────────────────────────────────────────────────

// newMux builds the routing tree with the given config and SSE hub, wiring
// the core to its production adapters. Extracted from main so integration
// tests can wire the handlers to an httptest.Server without starting the
// real listener.
func newMux(cfg config, hub *SSEHub) *http.ServeMux {
	return newMuxWithPortal(newPortal(cfg, hub), hub)
}

// newMuxWithPortal is the seam for tests that want to drive the HTTP surface
// with in-memory ports instead of real upstreams.
func newMuxWithPortal(p *Portal, hub *SSEHub) *http.ServeMux {
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
	giveH := p.handleGiveConsent()
	listH := p.handleListConsents()
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
	mux.HandleFunc("/portal/consents/", p.handleRevoke())
	mux.HandleFunc("/portal/events", handleSSE(hub))

	return mux
}
