// Package outway is the consumer's source, reached through its own FSC Outway.
// It is a driven adapter for consumer.Source.
//
// No separate token fetch is needed: the Outway picks a contract by
// grant-link, signs the FSC token internally and opens mTLS to the provider's
// Inway, which puts the request before the PEP and the PDP.
package outway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"gbo-demo/dienstverlener-backend/consumer"
	ldv "gbo-demo/ldv-client"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Source posts GraphQL to the Outway at URL: the Outway address plus the
// grant-link and backend path, e.g. http://hv-outway:8080/bri/graphql.
type Source struct {
	URL    string
	Client *http.Client
}

// Ask sends the question. The body is plain GraphQL with variables.bsn = PI;
// the sidecar at the source substitutes the BSN. 200 is an answer with data;
// any other status is a refusal, whose reason the Inway puts in `message`.
func (s *Source) Ask(ctx context.Context, q consumer.Question) (consumer.Answer, error) {
	body, err := json.Marshal(map[string]any{"query": q.Query, "variables": q.Variables})
	if err != nil {
		return consumer.Answer{}, fmt.Errorf("fsc_outway_call_failed: encode query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return consumer.Answer{}, fmt.Errorf("fsc_outway_call_failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Untrusted context header: X-GBO-Scope carries the requested scope.
	// (There is no X-GBO-Flow header and no flow grant property: the consent
	// token is what puts this request under the consent regime, #334.)
	req.Header.Set("X-GBO-Scope", q.Scope)
	req.Header.Set("X-GBO-Consent-Token", q.ConsentToken)
	req.Header.Set("Fsc-Transaction-Id", q.TransactionID)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	// The consumer's record of this call is where a reader starts the chain,
	// so the source's records hang under its span rather than the OTel one.
	if q.Parent.TraceID != "" {
		ldv.InjectTraceparent(req.Header, ldv.TraceContext{TraceID: q.Parent.TraceID, SpanID: q.Parent.SpanID, Sampled: true})
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return consumer.Answer{}, fmt.Errorf("fsc_outway_call_failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		return consumer.Answer{Allowed: true, Data: json.RawMessage(respBody)}, nil
	}
	return consumer.Answer{Reason: refusalReason(resp.StatusCode, respBody)}, nil
}

// refusalReason reads why the request was refused. The FSC Inway puts it in
// `message`; other upstreams use `reason`.
func refusalReason(status int, body []byte) string {
	var refusal struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &refusal)
	if refusal.Reason != "" {
		return refusal.Reason
	}
	if refusal.Message != "" {
		return refusal.Message
	}
	return fmt.Sprintf("upstream_error: status %d", status)
}
