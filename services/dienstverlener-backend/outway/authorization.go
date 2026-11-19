package outway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gbo-demo/dienstverlener-backend/consentrequest"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// maxAnswerBytes bounds what is read of an answer from the consent portal.
const maxAnswerBytes = 64 << 10

// ConsentPortal is the consent portal's FSC service consent-authorization,
// reached through the provider's Outway at URL (the Outway address plus the
// grant-link, e.g. http://hv-outway:8080/consent-authorization). It is a
// driven adapter for consentrequest.Portal.
type ConsentPortal struct {
	URL    string
	Client *http.Client
}

// Push sends the pushed authorization request (RFC 9126 §2.1).
func (p *ConsentPortal) Push(ctx context.Context, req consentrequest.PushedRequest) (string, error) {
	form := url.Values{
		"response_type":         {"code"},
		"client_id":             {req.ClientID},
		"service":               {req.Service},
		"scope":                 {strings.Join(req.Scopes, " ")},
		"redirect_uri":          {req.RedirectURI},
		"state":                 {req.State},
		"code_challenge":        {req.CodeChallenge},
		"code_challenge_method": {"S256"},
	}
	if req.ValidUntil != "" {
		form.Set("valid_until", req.ValidUntil)
	}
	var pushed struct {
		RequestURI string `json:"request_uri"`
	}
	if err := p.post(ctx, "/par", form, http.StatusCreated, &pushed); err != nil {
		return "", err
	}
	if pushed.RequestURI == "" {
		return "", fmt.Errorf("consent portal: pushed request answered without request_uri")
	}
	return pushed.RequestURI, nil
}

// Exchange redeems a code for the consent token (RFC 6749 §4.1.3, RFC 7636
// §4.5).
func (p *ConsentPortal) Exchange(ctx context.Context, req consentrequest.CodeExchange) (consentrequest.Consent, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {req.ClientID},
		"code":          {req.Code},
		"redirect_uri":  {req.RedirectURI},
		"code_verifier": {req.CodeVerifier},
	}
	var token struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		ConsentID   string `json:"consent_id"`
	}
	if err := p.post(ctx, "/token", form, http.StatusOK, &token); err != nil {
		return consentrequest.Consent{}, err
	}
	if token.AccessToken == "" {
		return consentrequest.Consent{}, fmt.Errorf("consent portal: token response without access_token")
	}
	return consentrequest.Consent{
		Token:     token.AccessToken,
		ConsentID: token.ConsentID,
		Scopes:    strings.Fields(token.Scope),
	}, nil
}

// post sends form to path and decodes an answer with status want into out.
// Any other answer with an OAuth error is a *consentrequest.Refusal.
func (p *ConsentPortal) post(ctx context.Context, path string, form url.Values, want int, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("consent portal: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := p.Client.Do(req)
	if err != nil {
		return fmt.Errorf("consent portal: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return fmt.Errorf("consent portal: read answer: %w", err)
	}
	if resp.StatusCode == want {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("consent portal: decode answer: %w", err)
		}
		return nil
	}
	var refusal struct {
		Error       string `json:"error"`
		Reason      string `json:"reason"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &refusal) == nil && refusal.Error != "" {
		return &consentrequest.Refusal{Code: refusal.Error, Reason: refusal.Reason, Description: refusal.Description}
	}
	return fmt.Errorf("consent portal: %s", refusalReason(resp.StatusCode, body))
}
