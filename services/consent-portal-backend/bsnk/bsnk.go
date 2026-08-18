// Package bsnk talks to the BSNk pseudonymisation mock. It is the only
// adapter that sees a plain BSN.
//
// The adapter translates representations and nothing else: no clock
// arithmetic, no interpretation of what a pseudonym means. Those are the
// core's job.
package bsnk

import (
	"context"
	"net/http"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/upstream"
)

// Client implements consent.Pseudonymizer over HTTP.
type Client struct {
	Base   string
	Caller upstream.Caller
}

func (c Client) Pseudonymize(ctx context.Context, bsn consent.BSN, recipientOIN string) (consent.Pseudonyms, error) {
	var out struct {
		Pseudonym string `json:"pseudonym"`
		PI        string `json:"pi"`
	}
	_, err := c.Caller.DoPrivate(ctx, "Pseudonymize BSN", http.MethodPost, c.Base+"/pseudonymize",
		map[string]any{"bsn": string(bsn), "recipient_oin": recipientOIN}, &out)
	if err != nil {
		return consent.Pseudonyms{}, err
	}
	return consent.Pseudonyms{Pseudonym: out.Pseudonym, PI: consent.PI(out.PI)}, nil
}
