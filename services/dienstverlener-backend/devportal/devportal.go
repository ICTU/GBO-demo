// Package devportal reports the consumer's runs to dev-portal-backend, so a
// flow a citizen drove appears in the developer timeline. It is a driven
// adapter for consumer.History.
package devportal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gbo-demo/dienstverlener-backend/consumer"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// postTimeout bounds one history post; it runs beside the answer, never in
// its way.
const postTimeout = 3 * time.Second

// History posts runs to the dev-portal backend at Base.
type History struct {
	Base string
}

// Observe posts the run in its own goroutine. Every failure is logged and
// dropped: the consumer's flow is the primary concern.
func (h *History) Observe(ctx context.Context, run consumer.Run) {
	// Skip when the dev-portal itself is the trigger: its frontend already
	// logs the run. That rule is knowledge about this sink, so it lives here
	// rather than in the core.
	if run.Request.FromDevPortal {
		return
	}
	// WithoutCancel keeps the trace context while surviving the handler
	// returning.
	go h.post(context.WithoutCancel(ctx), run)
}

func (h *History) post(ctx context.Context, run consumer.Run) {
	outcome := "deny"
	if run.Result.Allowed() {
		outcome = "allow"
	}
	entry := map[string]any{
		"scenario_name": fmt.Sprintf("Afnemer · use · scope %s", run.Request.ScopeID),
		"tab":           "use",
		// The payload replays the run; the consent token stays out of it.
		"payload": map[string]any{
			"consent_id":     run.Result.ConsentID,
			"scope_id":       run.Request.ScopeID,
			"belastingjaren": run.Request.Belastingjaren,
			"fields":         run.Request.Fields,
		},
		"trace_id":   run.Result.TraceID,
		"outcome":    outcome,
		"consent_id": run.Result.ConsentID,
		"response":   run.Result,
	}
	body, _ := json.Marshal(entry)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.Base+"/history", bytes.NewReader(body))
	if err != nil {
		slog.Warn("dev-portal-backend history post: build failed", "err", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	// Carry the trace across. Attaching the context alone is not enough: the
	// client below has a plain transport, so without an explicit inject the
	// post arrives at the dev-portal with no traceparent and shows up as an
	// orphan rather than a child of the run that produced it.
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	client := &http.Client{Timeout: postTimeout}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("dev-portal-backend history post: unreachable", "err", err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("dev-portal-backend history post: bad status", "status", resp.StatusCode)
	}
}
