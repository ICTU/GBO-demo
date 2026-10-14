// Package qtsp is the reference QTSP of the ASI route (ETSI TS 119 478): the
// client side of Discover (clause 5.3), Authorize (clause 6.1.3, with dynamic
// client registration, PKCE, private_key_jwt and DPoP) and Verify or Retrieve
// (clauses 6.1.1 and 6.1.2), and the check of the seal before a result is
// accepted.
//
// It discovers its way instead of being configured with URLs: the record of
// an attribute gives the data service's endpoint and contract, the contract
// names the authorization server's metadata, and the metadata the endpoints
// to register, authorize and get a token.
package qtsp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/qtsp-reference/internal/seal"
)

// httpInterface is the conformsTo of the HTTP-based interface (clause 6.1).
const httpInterface = "urn:ietf:rfc:9110"

const maxBody = 1 << 20

// Config is what a QTSP brings: where to discover, its certificate and key,
// its entry in the register of relying parties, where the user returns, and
// whom it trusts to seal results.
type Config struct {
	// DIP is the base URL of the Discover Interface.
	DIP string
	// HTTP carries every request; in compose it maps public URLs to
	// internal ones.
	HTTP *http.Client
	// Key and Chain are the QTSP's certificate key and chain, leaf first.
	Key   *ecdsa.PrivateKey
	Chain []*x509.Certificate
	// RegistryURI is the QTSP's entry in the register of relying parties.
	RegistryURI string
	// RedirectURI is where the user returns with the authorization code.
	RedirectURI string
	// SealAnchors are the roots a seal certificate must chain to.
	SealAnchors *x509.CertPool
}

// Client runs the flow. It registers once per authorization server and
// keeps one DPoP key for its tokens.
type Client struct {
	cfg  Config
	dpop *ecdsa.PrivateKey
	mu   sync.Mutex
	regs map[string]Registration
}

func New(cfg Config) (*Client, error) {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, dpop: key, regs: map[string]Registration{}}, nil
}

// --- Discover ----------------------------------------------------------------

// AuthorizationServer is what the QTSP uses of the RFC 8414 metadata.
type AuthorizationServer struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

// Plan is where one request goes: one data service, its contract and its
// authorization server.
type Plan struct {
	Attributes  []string
	EndpointURI string
	Contract    string
	Provider    string
	Server      AuthorizationServer
}

// NoDataServiceError: the Discover Interface knows no data service for the
// attribute.
type NoDataServiceError struct{ Attribute string }

func (e *NoDataServiceError) Error() string {
	return "no data service for attribute " + e.Attribute
}

// ErrNoCommonDataService: no single data service provides all attributes of
// the request; a token is for one data service, so one request is too.
var ErrNoCommonDataService = errors.New("no single data service provides all the attributes: request them separately")

// Plan discovers a data service that provides all the attributes, its
// contract and its authorization server. A directory can list several data
// services for an attribute (several ASIPs); the plan takes those that
// provide every requested attribute, and of those the first by endpoint, so
// the choice is the same on every run.
func (c *Client) Plan(ctx context.Context, attributes []string) (Plan, error) {
	if len(attributes) == 0 {
		return Plan{}, errors.New("no attributes")
	}
	type record struct{ endpoint, contract, provider string }
	var common map[string]record
	for i, id := range attributes {
		q := url.Values{"queryType": {"dataServices"}, "attributeIdentifier": {id}, "conformsTo": {httpInterface}}
		var out struct {
			DataServices []struct {
				EndpointURI         string `json:"endpointURI"`
				EndpointDescription string `json:"endpointDescription"`
				Provider            struct {
					LegalName string `json:"legalName"`
				} `json:"provider"`
			} `json:"dataServices"`
		}
		if err := c.getJSON(ctx, strings.TrimRight(c.cfg.DIP, "/")+"/retrieve?"+q.Encode(), &out); err != nil {
			return Plan{}, fmt.Errorf("discover: %w", err)
		}
		if len(out.DataServices) == 0 {
			return Plan{}, &NoDataServiceError{Attribute: id}
		}
		found := map[string]record{}
		for _, ds := range out.DataServices {
			found[ds.EndpointURI] = record{ds.EndpointURI, ds.EndpointDescription, ds.Provider.LegalName}
		}
		if i == 0 {
			common = found
			continue
		}
		for endpoint := range common {
			if _, ok := found[endpoint]; !ok {
				delete(common, endpoint)
			}
		}
	}
	if len(common) == 0 {
		return Plan{}, ErrNoCommonDataService
	}
	endpoints := make([]string, 0, len(common))
	for endpoint := range common {
		endpoints = append(endpoints, endpoint)
	}
	slices.Sort(endpoints)
	chosen := common[endpoints[0]]
	p := Plan{Attributes: attributes, EndpointURI: chosen.endpoint, Contract: chosen.contract, Provider: chosen.provider}
	var contract struct {
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Components struct {
			SecuritySchemes struct {
				OAuth2 struct {
					Metadata string `json:"x-oauth-authorization-server"`
				} `json:"oauth2"`
			} `json:"securitySchemes"`
		} `json:"components"`
	}
	if err := c.getJSON(ctx, p.Contract, &contract); err != nil {
		return Plan{}, fmt.Errorf("contract: %w", err)
	}
	if len(contract.Servers) != 1 || contract.Servers[0].URL != p.EndpointURI {
		return Plan{}, fmt.Errorf("contract %s does not describe the endpoint %s", p.Contract, p.EndpointURI)
	}
	metadata := contract.Components.SecuritySchemes.OAuth2.Metadata
	if metadata == "" {
		return Plan{}, fmt.Errorf("contract %s names no authorization server metadata", p.Contract)
	}
	if err := c.getJSON(ctx, metadata, &p.Server); err != nil {
		return Plan{}, fmt.Errorf("authorization server metadata: %w", err)
	}
	// RFC 8414 section 3.3: the metadata must be at the URL its issuer
	// gives (section 3.1).
	if want, err := MetadataURL(p.Server.Issuer); err != nil || metadata != want {
		return Plan{}, fmt.Errorf("authorization server metadata at %s names issuer %q", metadata, p.Server.Issuer)
	}
	return p, nil
}

// MetadataURL is where RFC 8414 section 3.1 puts the metadata of issuer: the
// well-known suffix between the host and the issuer's path, so issuer
// https://example.com/issuer1 has its metadata at
// https://example.com/.well-known/oauth-authorization-server/issuer1.
func MetadataURL(issuer string) (string, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme == "" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("issuer %q is not a URL without query or fragment", issuer)
	}
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-authorization-server" + strings.TrimRight(u.Path, "/"), nil
}

// --- Register ------------------------------------------------------------------

// Registration is the QTSP's client at one authorization server.
type Registration struct {
	ClientID string
	KeyID    string
}

// RegistrationError: the authorization server refused the registration.
type RegistrationError struct{ Code, Description string }

func (e *RegistrationError) Error() string {
	return "registration refused: " + e.Code + ": " + e.Description
}

// ErrUnavailable: a server could not be reached or answered 503; the step
// can be retried.
var ErrUnavailable = errors.New("unavailable")

// Register registers the QTSP at the plan's authorization server (RFC 7591)
// with a software statement sealed with its certificate, once per server.
func (c *Client) Register(ctx context.Context, as AuthorizationServer) (Registration, error) {
	c.mu.Lock()
	reg, ok := c.regs[as.Issuer]
	c.mu.Unlock()
	if ok {
		return reg, nil
	}
	now := time.Now()
	statement, err := c.sign(map[string]any{"x5c": x5c(c.cfg.Chain)}, "", map[string]any{
		"iss": "gbo-demo-reference-qtsp", "registryURI": c.cfg.RegistryURI,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		return Registration{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"redirect_uris":              []string{c.cfg.RedirectURI},
		"scope":                      "verify retrieve",
		"token_endpoint_auth_method": "private_key_jwt",
		"software_statement":         statement,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, as.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return Registration{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	status, raw, err := c.do(req)
	if err != nil {
		return Registration{}, err
	}
	switch {
	case status == http.StatusServiceUnavailable:
		return Registration{}, fmt.Errorf("%w: registration: %s", ErrUnavailable, oauthError(raw))
	case status != http.StatusCreated:
		var e struct {
			Code        string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e)
		return Registration{}, &RegistrationError{Code: e.Code, Description: e.Description}
	}
	// The registered key is the certificate's; the QTSP needs only its kid
	// for the client assertion.
	var out struct {
		ClientID string `json:"client_id"`
		JWKS     struct {
			Keys []struct {
				KeyID string `json:"kid"`
			} `json:"keys"`
		} `json:"jwks"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ClientID == "" || len(out.JWKS.Keys) == 0 {
		return Registration{}, errors.New("the registration response names no client_id or key")
	}
	reg = Registration{ClientID: out.ClientID, KeyID: out.JWKS.Keys[0].KeyID}
	c.mu.Lock()
	c.regs[as.Issuer] = reg
	c.mu.Unlock()
	return reg, nil
}

// --- Authorize -----------------------------------------------------------------

// PKCE returns a code verifier and its S256 challenge.
func PKCE() (verifier, challenge string) {
	verifier = randomID(32)
	d := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(d[:])
}

// State returns a fresh state value.
func State() string { return randomID(16) }

// AuthorizationURL is where the user's browser goes: the authorization
// request for operation (verify or retrieve) at the plan's data service.
func (c *Client) AuthorizationURL(p Plan, reg Registration, operation, state, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {reg.ClientID},
		"redirect_uri":          {c.cfg.RedirectURI},
		"scope":                 {operation},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {p.EndpointURI},
	}
	return p.Server.AuthorizationEndpoint + "?" + q.Encode()
}

// --- Token ---------------------------------------------------------------------

// TokenError: the token endpoint refused the code.
type TokenError struct{ Code, Description string }

func (e *TokenError) Error() string { return "token refused: " + e.Code + ": " + e.Description }

// Token exchanges the code for a DPoP-bound access token.
func (c *Client) Token(ctx context.Context, p Plan, reg Registration, code, verifier string) (string, error) {
	now := time.Now()
	assertion, err := c.sign(map[string]any{"kid": reg.KeyID}, "", map[string]any{
		"iss": reg.ClientID, "sub": reg.ClientID, "aud": p.Server.Issuer,
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "jti": randomID(16),
	})
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type":            {"authorization_code"},
		"code":                  {code},
		"redirect_uri":          {c.cfg.RedirectURI},
		"code_verifier":         {verifier},
		"client_id":             {reg.ClientID},
		"client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
		"client_assertion":      {assertion},
		"resource":              {p.EndpointURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Server.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	proof, err := c.proof(http.MethodPost, p.Server.TokenEndpoint, "")
	if err != nil {
		return "", err
	}
	req.Header.Set("DPoP", proof)
	status, raw, err := c.do(req)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		var e struct {
			Code        string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e)
		return "", &TokenError{Code: e.Code, Description: e.Description}
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" || !strings.EqualFold(out.TokenType, "DPoP") {
		return "", errors.New("the token response carries no DPoP access token")
	}
	return out.AccessToken, nil
}

// --- Verify and Retrieve -----------------------------------------------------

// Response is the ASIP's answer. A 200 is accepted only with a valid seal:
// then Seal is set; otherwise SealError says why it is not accepted.
type Response struct {
	Status    int
	Body      []byte
	Seal      *seal.Check
	SealError error
}

// Accepted says the QTSP may use the result: a 200 with a valid seal.
func (r Response) Accepted() bool { return r.Status == http.StatusOK && r.Seal != nil }

// Call posts body to operation (verify or retrieve) of the plan's data
// service with the token and a DPoP proof, and checks the seal of a 200.
func (c *Client) Call(ctx context.Context, p Plan, operation, token string, body []byte) (Response, error) {
	endpoint := strings.TrimRight(p.EndpointURI, "/") + "/" + operation
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	proof, err := c.proof(http.MethodPost, endpoint, token)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "DPoP "+token)
	req.Header.Set("DPoP", proof)
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Response{}, err
	}
	out := Response{Status: resp.StatusCode, Body: raw}
	if resp.StatusCode == http.StatusOK {
		check, err := seal.Validate(resp.Header.Get(seal.Header), raw, c.cfg.SealAnchors)
		if err != nil {
			out.SealError = err
		} else {
			out.Seal = &check
		}
	}
	return out, nil
}

// --- helpers -------------------------------------------------------------------

func (c *Client) getJSON(ctx context.Context, target string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	status, raw, err := c.do(req)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", target, status)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("GET %s: %w", target, err)
	}
	return nil
}

// do sends req; a transport failure is ErrUnavailable.
func (c *Client) do(req *http.Request) (int, []byte, error) {
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return resp.StatusCode, raw, err
}

func oauthError(raw []byte) string {
	var e struct {
		Code        string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &e)
	return strings.TrimSpace(e.Code + " " + e.Description)
}

// proof is a DPoP proof (RFC 9449 section 4.2) for method and target, bound
// to token when there is one.
func (c *Client) proof(method, target, token string) (string, error) {
	claims := map[string]any{"jti": randomID(16), "htm": method, "htu": target, "iat": time.Now().Unix()}
	if token != "" {
		d := sha256.Sum256([]byte(token))
		claims["ath"] = base64.RawURLEncoding.EncodeToString(d[:])
	}
	opts := (&jose.SignerOptions{EmbedJWK: true}).WithType("dpop+jwt")
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: c.dpop}, opts)
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}

// sign signs claims with the certificate key.
func (c *Client) sign(headers map[string]any, typ string, claims map[string]any) (string, error) {
	opts := &jose.SignerOptions{}
	if typ != "" {
		opts = opts.WithType(jose.ContentType(typ))
	}
	for k, v := range headers {
		opts = opts.WithHeader(jose.HeaderKey(k), v)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: c.cfg.Key}, opts)
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}

func x5c(chain []*x509.Certificate) []string {
	out := make([]string, len(chain))
	for i, c := range chain {
		out[i] = base64.StdEncoding.EncodeToString(c.Raw)
	}
	return out
}

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// --- request bodies ------------------------------------------------------------

// Attribute is an attribute the QTSP asks about, with the value it holds
// (verify) or without one (retrieve).
type Attribute struct {
	Identifier string
	Value      json.RawMessage
}

// VerifyBody is a verifyRequest (clause 6.1.1.1); with mandate it claims to
// act for another person, which this ASIP does not support (501).
func VerifyBody(attrs []Attribute, mandate bool) []byte {
	type attribute struct {
		AttributeIdentifier string          `json:"attributeIdentifier"`
		AttributeValue      json.RawMessage `json:"attributeValue"`
	}
	body := map[string]any{}
	list := []attribute{}
	for _, a := range attrs {
		list = append(list, attribute{a.Identifier, a.Value})
	}
	body["attributes"] = list
	if mandate {
		body["mandate"] = map[string]any{}
	}
	out, _ := json.Marshal(body)
	return out
}

// RetrieveBody is a retrieveRequest (clause 6.1.2.1).
func RetrieveBody(ids []string, mandate bool) []byte {
	type id struct {
		AttributeIdentifier string `json:"attributeIdentifier"`
	}
	list := []id{}
	for _, i := range ids {
		list = append(list, id{i})
	}
	body := map[string]any{"attributeIdentifiers": list}
	if mandate {
		body["mandate"] = map[string]any{}
	}
	out, _ := json.Marshal(body)
	return out
}
