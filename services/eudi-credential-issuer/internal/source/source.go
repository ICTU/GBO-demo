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
	"time"

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

// disclosedAttestations is the request of nl-wallet's issuance_server after
// a disclosure (attestation_data::disclosure::DisclosedAttestations): the
// disclosed attestations per DCQL credential query. Attributes are plain
// JSON values, as v0.5.0 (which eudi-issuance-server runs) sends them and
// the backends read them; v0.6.0 tags each value ({"type", "value"}).
type disclosedAttestations struct {
	ID           string                 `json:"id"`
	Attestations []disclosedAttestation `json:"attestations"`
}

type disclosedAttestation struct {
	AttestationType  string           `json:"attestation_type"`
	Format           string           `json:"format"`
	Attributes       map[string]any   `json:"attributes"`
	CA               string           `json:"ca"`
	IssuanceValidity issuanceValidity `json:"issuance_validity"`
	RevocationStatus string           `json:"revocation_status"`
}

type issuanceValidity struct {
	Signed     *time.Time `json:"signed,omitempty"`
	ValidFrom  *time.Time `json:"validFrom,omitempty"`
	ValidUntil *time.Time `json:"validUntil,omitempty"`
}

// FetchDisclosed posts the disclosed PID in the form of nl-wallet's
// issuance_server v0.5.0, so that a backend sees a wallet disclosure (and the
// QTSP callback, which accepts nothing else, serves it). The PID was
// verified, its status list included: revocation_status is Valid.
func (c *Client) FetchDisclosed(ctx context.Context, configurationID string, pid issuance.DisclosedPID, parameters map[string]string) ([]issuance.SourceDocument, error) {
	var validity issuanceValidity
	if !pid.Signed.IsZero() {
		validity.Signed = &pid.Signed
	}
	if !pid.ValidFrom.IsZero() {
		validity.ValidFrom = &pid.ValidFrom
	}
	if !pid.ValidUntil.IsZero() {
		validity.ValidUntil = &pid.ValidUntil
	}
	return c.post(ctx, configurationID, []disclosedAttestations{{ID: pid.QueryID, Attestations: []disclosedAttestation{{
		AttestationType: pid.AttestationType, Format: "dc+sd-jwt", Attributes: pid.Attributes,
		CA: pid.CA, IssuanceValidity: validity, RevocationStatus: "Valid",
	}}}}, parameters)
}

// Fetch posts {"subject": ...} to the configuration's endpoint, with the
// parameters in the query. The response is the IssuableDocument list the
// attestation server of nl-wallet's issuance_server returns. Backends that
// accept only a PID disclosure (the QTSP callback) do not serve this
// request; they get one with disclosure-based issuance.
func (c *Client) Fetch(ctx context.Context, configurationID string, subject map[string]any, parameters map[string]string) ([]issuance.SourceDocument, error) {
	return c.post(ctx, configurationID, subjectRequest{Subject: subject}, parameters)
}

// post sends request to the configuration's endpoint and reads the
// IssuableDocument list.
func (c *Client) post(ctx context.Context, configurationID string, request any, parameters map[string]string) ([]issuance.SourceDocument, error) {
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
	body, err := json.Marshal(request)
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
