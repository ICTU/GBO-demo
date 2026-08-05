// Package portalhttp is the driving side of the portal: everything that is
// true about HTTP and nothing that is true about consent. Handlers translate
// a request into a core call and a core result into a response; the domain
// rules live in the consent package.
package portalhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/logctx"
)

// ── HTTP helpers ──────────────────────────────────────────────────────────

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Demo-Session")
}

// WithDemoSession copies X-Demo-Session onto the server span, which is how
// the dev-portal's watch-mode tells one developer's run from another's. No
// consent rule reads it. Wrap INSIDE otelhttp, or there is no span yet.
func WithDemoSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := r.Header.Get("X-Demo-Session"); s != "" {
			trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("gbo.demo.session", s))
		}
		next.ServeHTTP(w, r)
	})
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
	DienstverlenrOIN string               `json:"dienstverlener_oin"`
	Scopes           []string             `json:"scopes"`
	ScopeEntries     []consent.ScopeEntry `json:"scope_entries"`
	ValiditySeconds  int                  `json:"validity_seconds,omitempty"`
}

type GiveConsentResponse struct {
	ConsentID string            `json:"consent_id"`
	Pseudonym string            `json:"pseudonym"`
	PI        string            `json:"pi"`
	TraceID   string            `json:"trace_id"`
	APICalls  []consent.APICall `json:"api_calls"`
}

// ── The citizen handler seam ──────────────────────────────────────────────

// citizenFunc is what a portal endpoint actually is: an authenticated citizen
// plus the request in, a JSON body out.
type citizenFunc func(ctx context.Context, citizen consent.BSN, r *http.Request) (any, error)

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
	case errors.Is(err, consent.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, consent.ErrNotOwned):
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}

type callLogKey struct{}

// callsFrom returns the upstream calls recorded so far in this request.
func callsFrom(ctx context.Context) []consent.APICall {
	if l, ok := ctx.Value(callLogKey{}).(*consent.CallLog); ok {
		return l.Snapshot()
	}
	return nil
}

func traceIDFrom(ctx context.Context) string {
	return trace.SpanContextFromContext(ctx).TraceID().String()
}

// citizen wraps one endpoint, adding — for every endpoint, automatically —
// CORS and OPTIONS, method gating, bearer parsing and JWT validation, the
// OTel span, the request-scoped observer, and error-to-status mapping.
//
// Adding a fourth portal endpoint costs a core method plus three lines here.
func citizen(method, op string, fn citizenFunc) http.HandlerFunc {
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
		log := &consent.CallLog{}
		ctx = consent.WithObserver(ctx, log)
		ctx = context.WithValue(ctx, callLogKey{}, log)

		res, err := fn(ctx, consent.BSN(bsn), r)
		if err != nil {
			var he *httpError
			if errors.As(err, &he) {
				writeJSON(w, he.Status, he.Body)
				return
			}
			body := map[string]any{"error": err.Error()}
			if errors.Is(err, consent.ErrNotOwned) {
				body["reason"] = "consent_not_owned_by_caller"
			}
			logctx.From(ctx).Error("portal."+op+" failed", "err", err.Error())
			writeJSON(w, statusFor(err), body)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
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
// domain rule, so routing it through consent.Portal would be a pure proxy.
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

func handleGiveConsent(p *consent.Portal) http.HandlerFunc {
	return citizen(http.MethodPost, "give_consent",
		func(ctx context.Context, citizen consent.BSN, r *http.Request) (any, error) {
			var req GiveConsentRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				return nil, &httpError{
					Status: http.StatusBadRequest,
					Body:   map[string]any{"error": "invalid request body"},
				}
			}
			in := consent.GiveInput{
				DienstverlenerOIN: req.DienstverlenrOIN,
				Scopes:            req.Scopes,
				ScopeEntries:      req.ScopeEntries,
				ValiditySeconds:   req.ValiditySeconds,
				Trigger:           r.Header.Get("X-Demo-Source"),
			}

			granted, err := p.GiveConsent(ctx, citizen, in)
			if err != nil {
				logctx.From(ctx).Error("give consent failed", "err", err.Error())
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
			p.Emit(ctx, consent.Event{
				Flow: "give_consent",
				Step: "flow_complete",
				Data: map[string]any{"trace_id": traceID},
				Summary: &consent.FlowSummary{
					Citizen:           citizen,
					DienstverlenerOIN: in.DienstverlenerOIN,
					Scopes:            in.Scopes,
					ValiditySeconds:   in.ValiditySeconds,
					TraceID:           traceID,
					ConsentID:         granted.ConsentID,
					Outcome:           "allow",
					Response:          resp,
					Trigger:           in.Trigger,
					DemoSession:       r.Header.Get("X-Demo-Session"),
				},
			})
			return resp, nil
		})
}

func handleListConsents(p *consent.Portal) http.HandlerFunc {
	return citizen(http.MethodGet, "list_consents",
		func(ctx context.Context, citizen consent.BSN, _ *http.Request) (any, error) {
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

func handleRevoke(p *consent.Portal) http.HandlerFunc {
	return citizen(http.MethodDelete, "revoke_consent",
		func(ctx context.Context, citizen consent.BSN, r *http.Request) (any, error) {
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
			p.Emit(ctx, consent.Event{
				Flow: "revoke_consent",
				Step: "flow_complete",
				Data: map[string]any{"trace_id": traceIDFrom(ctx)},
			})
			return map[string]string{"status": "REVOKED"}, nil
		})
}

// ── Routing ───────────────────────────────────────────────────────────────

// NewMux builds the routing tree over an already-wired core. Keeping this
// separate from main's wiring lets tests drive the HTTP surface with
// in-memory ports and no listener.
func NewMux(p *consent.Portal, hub *Hub) *http.ServeMux {
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
	giveH := handleGiveConsent(p)
	listH := handleListConsents(p)
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
	mux.HandleFunc("/portal/consents/", handleRevoke(p))
	mux.HandleFunc("/portal/events", handleSSE(hub))

	return mux
}
