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

// SubjectID is the id under which the subject is sent, in the place where
// the attestation server contract has the DCQL credential query id.
const SubjectID = "subject"

// Client is a bronpoort client.
type Client struct {
	HTTP *http.Client
}

type disclosedAttestations struct {
	ID           string        `json:"id"`
	Attestations []attestation `json:"attestations"`
}

type attestation struct {
	AttestationType string         `json:"attestation_type"`
	Attributes      map[string]any `json:"attributes"`
}

type issuableDocument struct {
	ID              string         `json:"id"`
	Format          string         `json:"format"`
	AttestationType string         `json:"attestation_type"`
	Attributes      map[string]any `json:"attributes"`
}

// Fetch posts the subject to endpoint, with parameters in the query.
// Demo shortcut: the request body carries the subject as attributes of one
// attestation, as a disclosure would; the backend cannot tell whether a
// wallet disclosed them or the offer caller supplied them. A real
// deployment needs an authenticated caller and a statement of origin.
func (c *Client) Fetch(ctx context.Context, endpoint string, subject map[string]any, parameters map[string]string) ([]issuance.SourceDocument, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: bad endpoint", issuance.ErrSourceUnavailable)
	}
	q := u.Query()
	for k, v := range parameters {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	body, err := json.Marshal([]disclosedAttestations{{
		ID:           SubjectID,
		Attestations: []attestation{{AttestationType: SubjectID, Attributes: subject}},
	}})
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
