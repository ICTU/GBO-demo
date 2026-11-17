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
	Reference                  string            `json:"reference,omitempty"`
}

func (c *Client) Offer(ctx context.Context, req testpid.OfferRequest) (map[string]string, error) {
	parameters := req.Parameters
	if parameters == nil {
		parameters = map[string]string{}
	}
	body, err := json.Marshal(offerBody{
		Profile:                    req.Profile,
		CredentialConfigurationIDs: req.ConfigurationIDs,
		Subject:                    map[string]string{"bsn": req.BSN},
		Parameters:                 parameters,
		Reference:                  req.Reference,
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

// Revocations revokes credentials through the issuer's internal API (POST
// /revocations), by offer reference or by status list entry.
type Revocations struct {
	url  string
	http *http.Client
}

// NewRevocations returns a client for the issuer's revocation endpoint, for
// example http://eudi-credential-issuer:8081/revocations.
func NewRevocations(revocationsURL string, timeout time.Duration) *Revocations {
	return &Revocations{url: revocationsURL, http: &http.Client{Timeout: timeout}}
}

// RevokeReference revokes every credential issued for offers with this
// reference, and returns how many status list entries that set.
func (c *Revocations) RevokeReference(ctx context.Context, reference string) (int, error) {
	answer, err := c.post(ctx, map[string]any{"reference": reference})
	if err != nil {
		return 0, err
	}
	var revoked struct {
		Revoked *int `json:"revoked"`
	}
	if err := json.Unmarshal(answer, &revoked); err != nil || revoked.Revoked == nil {
		return 0, fmt.Errorf("revocation answer: no count in %q", answer)
	}
	return *revoked.Revoked, nil
}

// RevokeEntry revokes one entry of a provider's status list.
func (c *Revocations) RevokeEntry(ctx context.Context, provider, list string, index int) error {
	_, err := c.post(ctx, map[string]any{"provider": provider, "list": list, "index": index})
	return err
}

func (c *Revocations) post(ctx context.Context, request map[string]any) ([]byte, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", testpid.ErrUnreachable, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", testpid.ErrUnreachable, err)
	}
	if resp.StatusCode/100 != 2 {
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(answer, &refusal)
		return nil, &testpid.Refusal{Status: resp.StatusCode, Code: refusal.Error}
	}
	return answer, nil
}
