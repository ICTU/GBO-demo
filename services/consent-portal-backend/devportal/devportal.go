// Package devportal reports completed citizen flows to dev-portal-backend so
// they appear in the developer timeline alongside developer-triggered runs.
//
// It is a driven adapter shaped as a consent.Observer: Observe returns
// nothing, so this sink cannot fail a citizen's request no matter how it
// misbehaves.
package devportal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"gbo-demo/consent-portal-backend/consent"
)

// History posts flow summaries to dev-portal-backend.
type History struct {
	Base   string
	Client *http.Client
}

func (h *History) Observe(ctx context.Context, e consent.Event) {
	if h == nil || h.Base == "" || e.Summary == nil {
		return
	}
	// Skip when the dev-portal itself is the trigger: its frontend already
	// logs the run. That rule is knowledge about this sink, so it lives here
	// rather than in the core.
	if e.Summary.Trigger == "dev-portal" {
		return
	}
	// WithoutCancel keeps the trace context while surviving the handler
	// returning. The previous implementation passed no context at all and
	// silently lost trace propagation.
	go h.post(context.WithoutCancel(ctx), *e.Summary)
}

func (h *History) post(ctx context.Context, s consent.FlowSummary) {
	entry := map[string]any{
		"scenario_name": fmt.Sprintf("Citizen · BSN %s", s.Citizen),
		"tab":           "issuance",
		"payload": map[string]any{
			"citizen_bsn":        string(s.Citizen),
			"dienstverlener_oin": s.DienstverlenerOIN,
			"scopes":             s.Scopes,
			"validity_seconds":   s.ValiditySeconds,
		},
		"trace_id":   s.TraceID,
		"outcome":    s.Outcome,
		"consent_id": s.ConsentID,
		"response":   s.Response,
	}
	body, err := json.Marshal(entry)
	if err != nil {
		slog.Warn("dev-portal-backend history post: marshal failed", "err", err.Error())
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Base+"/history", bytes.NewReader(body))
	if err != nil {
		slog.Warn("dev-portal-backend history post: build failed", "err", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.Client.Do(req)
	if err != nil {
		slog.Warn("dev-portal-backend history post: unreachable", "err", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("dev-portal-backend history post: bad status", "status", resp.StatusCode)
	}
}
