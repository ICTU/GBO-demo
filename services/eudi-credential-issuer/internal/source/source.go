// Package source calls the bronpoort over HTTP with the IssuableDocument
// contract that nl-wallet's issuance_server uses towards its attestation
// server, so backends built for that server (eudi-adapter, qtsp-reference)
// serve this issuer unchanged.
package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

// Client is a bronpoort client.
type Client struct {
	HTTP *http.Client
	// Endpoints maps a credential configuration id to the backend URL that
	// serves it.
	Endpoints map[string]string
}

// subjectRequest is what a pre-authorized offer sends: the subject the
// offer caller named. It is an object, so a backend cannot mistake it for a
// wallet disclosure, which is an array.
type subjectRequest struct {
	Subject map[string]any `json:"subject"`
}

type issuableDocument struct {
	ID              string         `json:"id"`
	Format          string         `json:"format"`
	AttestationType string         `json:"attestation_type"`
	Attributes      map[string]any `json:"attributes"`
}

// Fetch posts {"subject": ...} to the configuration's endpoint, with the
// parameters in the query. The response is the IssuableDocument list the
// attestation server of nl-wallet's issuance_server returns. Backends that
// accept only a PID disclosure (the QTSP callback) do not serve this
// request; they get one with disclosure-based issuance.
func (c *Client) Fetch(ctx context.Context, configurationID string, subject map[string]any, parameters map[string]string) ([]issuance.SourceDocument, error) {
	endpoint, ok := c.Endpoints[configurationID]
	if !ok {
		return nil, fmt.Errorf("%w: no endpoint for %s", issuance.ErrSourceUnavailable, configurationID)
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: bad endpoint", issuance.ErrSourceUnavailable)
	}
	q := u.Query()
	for k, v := range parameters {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	body, err := json.Marshal(subjectRequest{Subject: subject})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", issuance.ErrSourceUnavailable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, issuance.ErrNoData
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: status %d", issuance.ErrSourceUnavailable, resp.StatusCode)
	}
	var docs []issuableDocument
	// Numbers stay json.Number: whole numbers beyond 2^53 keep their value.
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	dec.UseNumber()
	if err := dec.Decode(&docs); err != nil {
		return nil, fmt.Errorf("%w: %v", issuance.ErrBadSourceData, err)
	}
	if len(docs) == 0 {
		return nil, issuance.ErrNoData
	}
	out := make([]issuance.SourceDocument, len(docs))
	for i, d := range docs {
		out[i] = issuance.SourceDocument{AttestationType: d.AttestationType, Format: d.Format, Attributes: d.Attributes}
	}
	return out, nil
}
