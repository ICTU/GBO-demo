// Package usageindex reads the consent usage index for the register, over
// the index's internal listener. Register and index are one organisation, so
// this is an internal call rather than an FSC service (#687 asks the latter;
// the plan chose this).
package usageindex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"consent-register/uses"
)

// Client implements uses.Index.
type Client struct {
	// URL is the index's internal listener, for example
	// http://consent-usage-index:4020.
	URL       string
	ReadToken string
	HTTP      *http.Client
}

// Registrations implements uses.Index.
func (c Client) Registrations(ctx context.Context, consentID string) ([]uses.Registration, error) {
	query := url.Values{"grondslag_type": {"consent"}, "grondslag_ref": {consentID}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(c.URL, "/")+"/registrations?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build index request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.ReadToken)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("reach the consent usage index: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("the consent usage index answered %d: %s", response.StatusCode, strings.TrimSpace(string(detail)))
	}
	var body struct {
		Registrations []struct {
			LogbookID    string `json:"logbook_id"`
			TraceID      string `json:"trace_id"`
			RegisteredAt string `json:"registered_at"`
		} `json:"registrations"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode the index answer: %w", err)
	}
	out := make([]uses.Registration, 0, len(body.Registrations))
	for _, r := range body.Registrations {
		at, err := time.Parse(time.RFC3339Nano, r.RegisteredAt)
		if err != nil {
			return nil, fmt.Errorf("the index sent registered_at %q: %w", r.RegisteredAt, err)
		}
		out = append(out, uses.Registration{LogbookID: r.LogbookID, TraceID: r.TraceID, RegisteredAt: at})
	}
	return out, nil
}
