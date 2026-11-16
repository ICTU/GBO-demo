package portalhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/logctx"
)

// The citizen's side of a pushed request: the portal resolves the
// reference the browser brought and records the citizen's decision. The
// decision answers with the return URL the browser goes to; the frontend
// only follows it.

// maxDecisionBytes bounds a decision's body.
const maxDecisionBytes = 16 << 10

// ConsentRequestResponse is a resolved reference: what the citizen is
// asked to consent to.
type ConsentRequestResponse struct {
	ClientID   string   `json:"client_id"`
	ClientName string   `json:"client_name"`
	Service    string   `json:"service"`
	Purpose    string   `json:"purpose"`
	Scopes     []string `json:"scopes"`
	ValidUntil string   `json:"valid_until,omitempty"` // RFC 3339
}

// DecisionRequest is the citizen's decision: "grant" with the data sets
// consented to, or "deny".
type DecisionRequest struct {
	Decision string   `json:"decision"`
	Scopes   []string `json:"scopes"`
}

// DecisionResponse says how the decision ended and where the browser goes.
// It never carries the consent token.
type DecisionResponse struct {
	Outcome    string `json:"outcome"`
	ClientName string `json:"client_name"`
	RedirectTo string `json:"redirect_to"`
}

// handleResolveRequest serves GET /portal/requests/{ref}?client_id=…
func handleResolveRequest(p *consent.Portal) http.HandlerFunc {
	return citizen(http.MethodGet, "resolve_request",
		func(ctx context.Context, citizen consent.BSN, r *http.Request) (any, error) {
			request, err := p.ResolveRequest(ctx, citizen, r.PathValue("ref"), r.URL.Query().Get("client_id"))
			if err != nil {
				return nil, referenceError(ctx, err)
			}
			resp := ConsentRequestResponse{
				ClientID:   string(request.ClientID),
				ClientName: request.ClientName,
				Service:    request.Service,
				Purpose:    request.Purpose,
				Scopes:     request.Scopes,
			}
			if !request.ValidUntil.IsZero() {
				resp.ValidUntil = request.ValidUntil.UTC().Format(time.RFC3339)
			}
			return resp, nil
		})
}

// handleDecision serves POST /portal/requests/{ref}/decision.
func handleDecision(p *consent.Portal) http.HandlerFunc {
	return citizen(http.MethodPost, "decide_request",
		func(ctx context.Context, citizen consent.BSN, r *http.Request) (any, error) {
			var req DecisionRequest
			if err := json.NewDecoder(io.LimitReader(r.Body, maxDecisionBytes)).Decode(&req); err != nil ||
				(req.Decision != "grant" && req.Decision != "deny") {
				return nil, &httpError{
					Status: http.StatusBadRequest,
					Body:   map[string]any{"error": `the body must be {"decision": "grant" or "deny", "scopes": [...]}`},
				}
			}

			decided, err := p.Decide(ctx, citizen, r.PathValue("ref"), consent.Decision{
				Grant:  req.Decision == "grant",
				Scopes: req.Scopes,
			})
			if errors.Is(err, consent.ErrInvalidDecision) {
				return nil, &httpError{Status: http.StatusBadRequest, Body: map[string]any{"error": err.Error()}}
			}
			if err != nil {
				return nil, referenceError(ctx, err)
			}

			params := url.Values{"state": {decided.State}}
			switch decided.Outcome {
			case consent.OutcomeGranted:
				params.Set("code", decided.Code)
			case consent.OutcomeDenied:
				params.Set("error", "access_denied")
			default:
				logctx.From(ctx).Error("consent decision failed", "err", decided.Failure.Error())
				params.Set("error", "server_error")
			}
			p.Emit(ctx, consent.Event{
				Flow: "give_consent",
				Step: "flow_complete",
				Data: map[string]any{"trace_id": traceIDFrom(ctx), "outcome": string(decided.Outcome)},
			})
			redirectTo, err := withQuery(decided.ReturnURL, params)
			if err != nil {
				return nil, err
			}
			return DecisionResponse{
				Outcome:    string(decided.Outcome),
				ClientName: decided.ClientName,
				RedirectTo: redirectTo,
			}, nil
		})
}

// withQuery adds params to the query of a registered return URL and keeps
// the query it already has (RFC 6749 §3.1.2).
func withQuery(returnURL string, params url.Values) (string, error) {
	u, err := url.Parse(returnURL)
	if err != nil {
		return "", err
	}
	if u.RawQuery != "" {
		u.RawQuery += "&"
	}
	u.RawQuery += params.Encode()
	return u.String(), nil
}

// referenceError maps a refused reference to its status: unknown 404,
// expired or spent 410, another client ID 400, another citizen 403. The
// body names the reason code.
func referenceError(ctx context.Context, err error) error {
	var refusal *consent.Refusal
	if !errors.As(err, &refusal) {
		return err
	}
	status := http.StatusBadRequest
	switch refusal.Code {
	case consent.ReasonRequestURIUnknown:
		status = http.StatusNotFound
	case consent.ReasonRequestURIExpired, consent.ReasonRequestURISpent:
		status = http.StatusGone
	case consent.ReasonRequestURIOtherCitizen:
		status = http.StatusForbidden
	}
	logctx.From(ctx).Warn("consent request refused", "reason", string(refusal.Code), "err", refusal.Error())
	return &httpError{Status: status, Body: map[string]any{"error": refusal.Detail, "reason": string(refusal.Code)}}
}
