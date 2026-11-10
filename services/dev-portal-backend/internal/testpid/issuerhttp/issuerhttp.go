// Package issuerhttp asks the own credential issuer for offers through its
// internal API (POST /offers). That API has no authentication and is only
// reachable on the compose network; the browser never reaches it.
package issuerhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gbo-demo/dev-portal-backend/internal/testpid"
)

// Client implements testpid.Issuer.
type Client struct {
	offersURL string
	http      *http.Client
}

// New returns a client for the issuer's offer endpoint, for example
// http://eudi-credential-issuer:8081/offers.
func New(offersURL string, timeout time.Duration) *Client {
	return &Client{offersURL: offersURL, http: &http.Client{Timeout: timeout}}
}

type offerBody struct {
	Profile                    string            `json:"profile"`
	CredentialConfigurationIDs []string          `json:"credential_configuration_ids"`
	Subject                    map[string]string `json:"subject"`
	Parameters                 map[string]string `json:"parameters"`
}

func (c *Client) Offer(ctx context.Context, req testpid.OfferRequest) (map[string]string, error) {
	body, err := json.Marshal(offerBody{
		Profile:                    req.Profile,
		CredentialConfigurationIDs: req.ConfigurationIDs,
		Subject:                    map[string]string{"bsn": req.BSN},
		Parameters:                 map[string]string{},
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.offersURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", testpid.ErrUnreachable, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", testpid.ErrUnreachable, err)
	}
	if resp.StatusCode != http.StatusCreated {
		var refusal struct {
			Error string `json:"error"`
		}
		// An answer that is no JSON error, from a proxy say, has no code.
		_ = json.Unmarshal(answer, &refusal)
		return nil, &testpid.Refusal{Status: resp.StatusCode, Code: refusal.Error}
	}
	var offer struct {
		Links map[string]string `json:"links"`
	}
	if err := json.Unmarshal(answer, &offer); err != nil {
		return nil, fmt.Errorf("offer answer: %w", err)
	}
	return offer.Links, nil
}
