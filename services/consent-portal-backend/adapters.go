package main

// Driven (outbound) adapters. An adapter is a plain struct holding a base URL
// and an *http.Client — there is no "service" type wrapping it, because a
// method whose body is a single delegating call earns nothing.
//
// Adapters translate representations. They do not decide outcomes: no clock
// arithmetic, no status interpretation, no comparison of who is calling. If
// you find a time.Now() or a `== "REVOKED"` in this file, that is the bug —
// that logic belongs in portal.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// caller performs JSON round-trips and reports each one to the request's
// observer, so upstream-call cards are captured once here instead of being
// hand-assembled at every call site.
type caller struct {
	client *http.Client
}

// do issues one JSON request. label is the human name shown on the dev-portal
// card. It returns the HTTP status alongside the error so callers can map 404
// to a domain error without parsing strings.
func (c caller) do(ctx context.Context, label, method, rawURL string, body, result any) (int, error) {
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
	resp, err := c.client.Do(req)
	durationMS := time.Since(start).Milliseconds()

	if err != nil {
		// A transport failure has no status; the panel shows it as 502.
		observerFrom(ctx).Observe(ctx, Event{
			Call: newAPICall(label, method, rawURL, http.StatusBadGateway, reqBytes, nil, durationMS),
		})
		return 0, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	observerFrom(ctx).Observe(ctx, Event{
		Call: newAPICall(label, method, rawURL, resp.StatusCode, reqBytes, respBytes, durationMS),
	})

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

// ── BSNk ──────────────────────────────────────────────────────────────────

type bsnkClient struct {
	base string
	caller
}

func (b bsnkClient) Pseudonymize(ctx context.Context, bsn BSN, recipientOIN string) (Pseudonyms, error) {
	var out struct {
		Pseudonym string `json:"pseudonym"`
		PI        string `json:"pi"`
	}
	_, err := b.do(ctx, "Pseudonymize BSN", http.MethodPost, b.base+"/pseudonymize",
		map[string]any{"bsn": string(bsn), "recipient_oin": recipientOIN}, &out)
	if err != nil {
		return Pseudonyms{}, err
	}
	return Pseudonyms{Pseudonym: out.Pseudonym, PI: PI(out.PI)}, nil
}

// ── consent-register ──────────────────────────────────────────────────────

type registerClient struct {
	base string
	caller
}

func (r registerClient) Create(ctx context.Context, c NewConsent) (ConsentRecord, error) {
	var out struct {
		ConsentID string `json:"consent_id"`
	}
	// The wire field is "pi": the register's subject is a PI and nothing else.
	_, err := r.do(ctx, "Create Consent", http.MethodPost, r.base+"/consents", map[string]any{
		"pi":                 string(c.Subject),
		"dienstverlener_oin": c.DienstverlenerOIN,
		"scopes":             c.Scopes,
		"scope_entries":      c.ScopeEntries,
		"use_case":           c.UseCase,
		"validity_seconds":   c.ValiditySeconds,
	}, &out)
	if err != nil {
		return ConsentRecord{}, err
	}
	return ConsentRecord{ID: out.ConsentID, Subject: c.Subject}, nil
}

func (r registerClient) ListBySubject(ctx context.Context, subject PI) ([]ConsentRecord, error) {
	var raw []map[string]any
	_, err := r.do(ctx, "List Consents", http.MethodGet,
		r.base+"/consents?pi="+url.QueryEscape(string(subject)), nil, &raw)
	if err != nil {
		return nil, err
	}
	recs := make([]ConsentRecord, 0, len(raw))
	for _, m := range raw {
		recs = append(recs, recordFromRaw(m))
	}
	return recs, nil
}

func (r registerClient) Get(ctx context.Context, consentID string) (ConsentRecord, error) {
	var raw map[string]any
	status, err := r.do(ctx, "Get Consent", http.MethodGet, r.base+"/consents/"+consentID, nil, &raw)
	if err != nil {
		if status == http.StatusNotFound {
			return ConsentRecord{}, ErrConsentNotFound
		}
		return ConsentRecord{}, err
	}
	return recordFromRaw(raw), nil
}

func (r registerClient) Revoke(ctx context.Context, consentID string) error {
	status, err := r.do(ctx, "Revoke Consent", http.MethodDelete, r.base+"/consents/"+consentID, nil, nil)
	if err != nil {
		if status == http.StatusNotFound {
			return ErrConsentNotFound
		}
		return err
	}
	return nil
}

// recordFromRaw decodes a register record, keeping the original payload so
// the UI still sees fields this service does not model. Parsing a timestamp
// is representation translation, which is why it belongs here; deciding what
// the timestamp *means* is EffectiveStatus's job, in the core.
func recordFromRaw(raw map[string]any) ConsentRecord {
	rec := ConsentRecord{Raw: raw}
	if v, ok := raw["consent_id"].(string); ok {
		rec.ID = v
	}
	if v, ok := raw["status"].(string); ok {
		rec.Status = v
	}
	if v, ok := raw["pi"].(string); ok {
		rec.Subject = PI(v)
	}
	if v, ok := raw["valid_until"].(string); ok {
		// The register has emitted both layouts; try the stricter one first.
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			rec.ValidUntil = t
		} else if t, err := time.Parse(time.RFC3339, v); err == nil {
			rec.ValidUntil = t
		}
	}
	return rec
}
