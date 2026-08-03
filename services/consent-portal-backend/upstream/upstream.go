// Package upstream performs JSON calls to the portal's own downstream
// services and reports each one to the request's observer, so upstream-call
// cards are captured in one place instead of at every call site.
//
// It exists because bsnk and register would otherwise hold the same fifty
// lines twice. It is deliberately thin: it knows about JSON, timing and trace
// propagation, and nothing about consent.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"gbo-demo/consent-portal-backend/consent"
)

// Caller issues JSON requests over a shared client.
type Caller struct {
	Client *http.Client
}

// Do issues one JSON request. label is the human name shown on the dev-portal
// card. It returns the HTTP status alongside the error so callers can map 404
// to a domain error without parsing strings.
func (c Caller) Do(ctx context.Context, label, method, rawURL string, body, result any) (int, error) {
	var reqBytes []byte
	var reader io.Reader
	if body != nil {
		var err error
		reqBytes, err = json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("marshal: %w", err)
		}
		reader = bytes.NewReader(reqBytes)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return 0, fmt.Errorf("new request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	start := time.Now()
	resp, err := c.Client.Do(req)
	durationMS := time.Since(start).Milliseconds()

	if err != nil {
		// A transport failure has no status; the panel shows it as 502.
		c.report(ctx, label, method, rawURL, http.StatusBadGateway, reqBytes, nil, durationMS)
		return 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	c.report(ctx, label, method, rawURL, resp.StatusCode, reqBytes, respBytes, durationMS)

	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, string(respBytes))
	}
	if result != nil {
		if err := json.Unmarshal(respBytes, result); err != nil {
			return resp.StatusCode, fmt.Errorf("decode: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func (c Caller) report(ctx context.Context, label, method, url string, status int, reqBody, respBody []byte, durationMS int64) {
	consent.ObserverFrom(ctx).Observe(ctx, consent.Event{
		Call: consent.NewAPICall(uuid.New().String(), label, method, url, status, reqBody, respBody, durationMS),
	})
}
