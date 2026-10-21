package walletverifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// Means is the name the core gives the NL Wallet: in the identity, and so in
// the token's acr and amr.
const Means = "nl-wallet"

// Client is the adapter of the core's SessionIdentifier port for the NL
// Wallet: it opens a disclosure session at the verifier, follows it, and
// takes the disclosed PID as the identity of the user.
type Client struct {
	// Internal is the verifier's internal API (sessions, disclosed
	// attributes); Public its wallet API, where the session status is.
	Internal, Public string
	APIKey           string
	HTTP             *http.Client
}

// New returns a client for the verifier at internal and public.
func New(internal, public, apiKey string) *Client {
	return &Client{Internal: strings.TrimRight(internal, "/"), Public: strings.TrimRight(public, "/"), APIKey: apiKey,
		HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// query asks the wallet for the PID claims of PIDType.
func query() map[string]any {
	claims := make([]map[string]any, len(PIDClaims))
	for i, c := range PIDClaims {
		claims[i] = map[string]any{"path": []string{c}}
	}
	return map[string]any{"credentials": []map[string]any{{
		"id": "pid", "format": "dc+sd-jwt", "meta": map[string]any{"vct_values": []string{PIDType}}, "claims": claims,
	}}}
}

func (c *Client) Start(ctx context.Context, returnURLTemplate string) (string, error) {
	request := map[string]any{"usecase": Usecase, "dcql_query": query()}
	if returnURLTemplate != "" {
		request["return_url_template"] = returnURLTemplate
	}
	body, _ := json.Marshal(request)
	var out struct {
		SessionToken string `json:"session_token"`
	}
	if err := c.do(ctx, http.MethodPost, c.Internal+"/disclosure/sessions", body, true, &out); err != nil {
		return "", err
	}
	if out.SessionToken == "" {
		return "", errors.New("the verifier returned no session token")
	}
	return out.SessionToken, nil
}

func (c *Client) Progress(ctx context.Context, session string) (authorization.Progress, error) {
	cross, err := c.status(ctx, session, "cross_device")
	if err != nil {
		return authorization.Progress{}, err
	}
	current, err := progress(cross)
	if err != nil || cross.Status != "CREATED" || cross.UL == "" {
		return current, err
	}
	same, err := c.status(ctx, session, "same_device")
	if err != nil {
		return current, nil
	}
	if same.Status != "CREATED" {
		updated, err := progress(same)
		if err != nil {
			return current, nil
		}
		return updated, nil
	}
	current.SameDeviceLink = same.UL
	return current, nil
}

type statusResponse struct {
	Status string `json:"status"`
	UL     string `json:"ul"`
}

func (c *Client) status(ctx context.Context, session, sessionType string) (statusResponse, error) {
	var out statusResponse
	target := c.Public + "/disclosure/sessions/" + url.PathEscape(session) + "?session_type=" + sessionType
	if err := c.do(ctx, http.MethodGet, target, nil, false, &out); err != nil {
		return statusResponse{}, err
	}
	return out, nil
}

func progress(out statusResponse) (authorization.Progress, error) {
	switch out.Status {
	case "CREATED", "WAITING_FOR_RESPONSE":
		return authorization.Progress{State: authorization.SessionWaiting, Link: out.UL}, nil
	case "DONE":
		return authorization.Progress{State: authorization.SessionDone}, nil
	case "FAILED", "CANCELLED", "EXPIRED":
		return authorization.Progress{State: authorization.SessionFailed}, nil
	}
	return authorization.Progress{}, fmt.Errorf("unknown session status %q", out.Status)
}

// Identify fetches the disclosed attributes of the finished session and
// takes the PID: a disclosure of PIDType with a BSN. Anything else does not
// identify the user; a verifier that cannot be reached, fails or refuses the
// API key is ErrMeansUnavailable.
func (c *Client) Identify(ctx context.Context, session string) (authorization.Identity, error) {
	return c.identify(ctx, session, "")
}

// IdentifyReturn fetches the disclosed PID after a same-device return. The
// verifier releases it only for the matching nonce it added to the return
// URL. A rejected nonce is a failed identification; verifier or API-key
// failures remain an unavailable means.
func (c *Client) IdentifyReturn(ctx context.Context, session, nonce string) (authorization.Identity, error) {
	if nonce == "" {
		return authorization.Identity{}, errors.New("the return nonce is missing")
	}
	return c.identify(ctx, session, nonce)
}

func (c *Client) identify(ctx context.Context, session, nonce string) (authorization.Identity, error) {
	var out []struct {
		ID           string `json:"id"`
		Attestations []struct {
			Type       string          `json:"attestation_type"`
			Format     string          `json:"format"`
			Attributes json.RawMessage `json:"attributes"`
		} `json:"attestations"`
	}
	target := c.Internal + "/disclosure/sessions/" + url.PathEscape(session) + "/disclosed_attributes"
	if nonce != "" {
		target += "?" + url.Values{"nonce": {nonce}}.Encode()
	}
	if err := c.do(ctx, http.MethodGet, target, nil, true, &out); err != nil {
		var responseErr *verifierResponseError
		if nonce != "" && errors.As(err, &responseErr) && responseErr.Status == http.StatusUnauthorized && responseErr.errorType() == "nonce" {
			return authorization.Identity{}, errors.New("the verifier rejected the return nonce")
		}
		return authorization.Identity{}, err
	}
	for _, d := range out {
		for _, a := range d.Attestations {
			if d.ID != "pid" || a.Type != PIDType {
				continue
			}
			var pid struct {
				BSN           string   `json:"bsn"`
				FamilyName    string   `json:"family_name"`
				GivenName     string   `json:"given_name"`
				Birthdate     string   `json:"birthdate"`
				Nationalities []string `json:"nationalities"`
			}
			if err := json.Unmarshal(a.Attributes, &pid); err != nil {
				return authorization.Identity{}, fmt.Errorf("the PID is not readable: %w", err)
			}
			if pid.BSN == "" {
				return authorization.Identity{}, errors.New("the PID has no BSN")
			}
			return authorization.Identity{BSN: pid.BSN, FamilyName: pid.FamilyName, GivenName: pid.GivenName,
				Birthdate: pid.Birthdate, Nationalities: pid.Nationalities}, nil
		}
	}
	return authorization.Identity{}, fmt.Errorf("no disclosure of %s", PIDType)
}

type verifierResponseError struct {
	Method, Path string
	Status       int
	Body         []byte
	Unavailable  bool
}

func (e *verifierResponseError) Error() string {
	return fmt.Sprintf("%s %s: %d", e.Method, e.Path, e.Status)
}

func (e *verifierResponseError) Unwrap() error {
	if e.Unavailable {
		return authorization.ErrMeansUnavailable
	}
	return nil
}

func (e *verifierResponseError) errorType() string {
	var body struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(e.Body, &body)
	return body.Type
}

func (c *Client) do(ctx context.Context, method, target string, body []byte, internal bool, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if internal {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", authorization.ErrMeansUnavailable, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode >= 500, resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		// The verifier fails, or refuses our API key: it is unavailable,
		// not a disclosure that does not identify the user.
		return &verifierResponseError{Method: method, Path: req.URL.Path, Status: resp.StatusCode, Body: raw, Unavailable: true}
	default:
		return &verifierResponseError{Method: method, Path: req.URL.Path, Status: resp.StatusCode, Body: raw}
	}
	return json.Unmarshal(raw, out)
}
