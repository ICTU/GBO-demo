// Package register talks to the consent register, which stores a portal-scoped
// subject reference and sees PI only transiently while issuing a signed token.
// There is no method here that accepts a BSN, and there must never be one.
//
// The adapter translates representations; it does not decide outcomes. No
// time.Now(), no comparison against "REVOKED", no notion of who is calling.
// If that logic appears in this file, it is a bug — it belongs in the consent
// package.
package register

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/upstream"
)

// Client implements consent.Store over HTTP.
type Client struct {
	Base   string
	Caller upstream.Caller
}

func (c Client) Create(ctx context.Context, d consent.Draft) (consent.Record, error) {
	var out struct {
		ConsentID    string `json:"consent_id"`
		ConsentToken string `json:"consent_token"`
	}
	// PI is transient token material. subject_ref is the only subject value the consent register
	// persists and returns.
	_, err := c.Caller.DoPrivate(ctx, "Create Consent", http.MethodPost, c.Base+"/consents", map[string]any{
		"pi":                 string(d.PI),
		"subject_ref":        string(d.SubjectRef),
		"dienstverlener_oin": d.DienstverlenerOIN,
		"scopes":             d.Scopes,
		"scope_entries":      d.ScopeEntries,
		"use_case":           d.UseCase,
		"validity_seconds":   d.ValiditySeconds,
	}, &out)
	if err != nil {
		return consent.Record{}, err
	}
	return consent.Record{ID: out.ConsentID, SubjectRef: d.SubjectRef, Token: out.ConsentToken}, nil
}

func (c Client) ListBySubject(ctx context.Context, subject consent.SubjectRef) ([]consent.Record, error) {
	var raw []map[string]any
	_, err := c.Caller.Do(ctx, "List Consents", http.MethodGet,
		c.Base+"/consents?subject_ref="+url.QueryEscape(string(subject)), nil, &raw)
	if err != nil {
		return nil, err
	}
	recs := make([]consent.Record, 0, len(raw))
	for _, m := range raw {
		recs = append(recs, recordFromRaw(m))
	}
	return recs, nil
}

func (c Client) Get(ctx context.Context, consentID string) (consent.Record, error) {
	var raw map[string]any
	status, err := c.Caller.Do(ctx, "Get Consent", http.MethodGet, c.Base+"/consents/"+consentID, nil, &raw)
	if err != nil {
		if status == http.StatusNotFound {
			return consent.Record{}, consent.ErrNotFound
		}
		return consent.Record{}, err
	}
	return recordFromRaw(raw), nil
}

func (c Client) Revoke(ctx context.Context, consentID string) error {
	status, err := c.Caller.Do(ctx, "Revoke Consent", http.MethodDelete, c.Base+"/consents/"+consentID, nil, nil)
	if err != nil {
		if status == http.StatusNotFound {
			return consent.ErrNotFound
		}
		return err
	}
	return nil
}

// recordFromRaw decodes a register record, keeping the original payload so
// the UI still sees fields this service does not model. Parsing a timestamp
// is representation translation, which is why it belongs here; deciding what
// the timestamp means is EffectiveStatus's job, in the core.
func recordFromRaw(raw map[string]any) consent.Record {
	rec := consent.Record{Raw: raw}
	if v, ok := raw["consent_id"].(string); ok {
		rec.ID = v
	}
	if v, ok := raw["status"].(string); ok {
		rec.Status = v
	}
	if v, ok := raw["subject_ref"].(string); ok {
		rec.SubjectRef = consent.SubjectRef(v)
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
