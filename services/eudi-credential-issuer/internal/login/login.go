// Package login is the issuer as OAuth client of GBO's authorization server,
// where the citizen logs in before a source's attestation is issued with the
// authorization code flow (#614). Each provider is a client of its own,
// registered with its access certificate (RFC 7591 with a software
// statement, as a QTSP registers for the ASI), authenticated with
// private_key_jwt, and its tokens are DPoP-bound. The issuer is also the
// resource server of these tokens: it checks them itself and takes the BSN.
package login

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
)

// Scope is the scope of a credential issuer at the authorization server.
const Scope = "issuance"

// BSNClaim carries the citizen's BSN in the access token.
const BSNClaim = "https://gbo.example/claims/bsn"

const (
	clientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"
	leeway              = 30 * time.Second
	maxBody             = 1 << 20
)

// ErrUnavailable means the authorization server could not be reached, failed,
// or no longer knows the client: the citizen can try again later. It is the
// core's ErrLoginUnavailable, so the core tells it from a refused login.
var ErrUnavailable = issuance.ErrLoginUnavailable

// Provider is a provider as client: its access certificate, its entry in the
// register of wallet-relying parties and its callback.
type Provider struct {
	Name        string
	Key         crypto.Signer
	Chain       []*x509.Certificate
	RegistryURI string
	RedirectURI string
}

// Client is the issuer's client of one authorization server.
type Client struct {
	server    string
	http      *http.Client
	providers map[string]Provider
	dpop      *ecdsa.PrivateKey
	jkt       string
	now       func() time.Time

	mu   sync.Mutex
	regs map[string]registration
}

// registration is a provider's client at the server, and the server's keys
// when it registered: other keys mean the server restarted.
type registration struct {
	clientID string
	keyID    string
	keys     string
}

// New returns a client of the authorization server with issuer identifier
// server. httpClient reaches it; it may map the public address to an
// internal one, the request keeps its URL for what is signed.
func New(server string, httpClient *http.Client, providers []Provider) (*Client, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	thumbprint, err := (&jose.JSONWebKey{Key: &key.PublicKey}).Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, err
	}
	c := &Client{server: strings.TrimRight(server, "/"), http: httpClient, providers: map[string]Provider{}, dpop: key,
		jkt: base64.RawURLEncoding.EncodeToString(thumbprint), now: time.Now, regs: map[string]registration{}}
	for _, p := range providers {
		c.providers[p.Name] = p
	}
	return c, nil
}

type serverMetadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

// AuthorizationURL returns where the browser goes to log in for provider:
// the authorization request for resource and the credential configurations,
// with state and the S256 PKCE challenge. It registers the provider first when the server does not know
// it yet.
func (c *Client) AuthorizationURL(ctx context.Context, provider, resource string, configurations []string, state, challenge string) (string, error) {
	p, ok := c.providers[provider]
	if !ok {
		return "", fmt.Errorf("provider %q is not a client of the authorization server", provider)
	}
	meta, err := c.metadata(ctx)
	if err != nil {
		return "", err
	}
	keys, err := c.keys(ctx, meta)
	if err != nil {
		return "", err
	}
	reg, err := c.registration(ctx, p, meta, keys)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {reg.clientID},
		"redirect_uri":          {p.RedirectURI},
		"scope":                 {Scope},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}
	// authorization_details name the credential configurations (RFC 9396,
	// OpenID4VCI 1.0 section 5.1.1), so the consent can name them.
	if len(configurations) > 0 {
		type detail struct {
			Type                      string `json:"type"`
			CredentialConfigurationID string `json:"credential_configuration_id"`
		}
		details := make([]detail, 0, len(configurations))
		for _, id := range configurations {
			details = append(details, detail{Type: "openid_credential", CredentialConfigurationID: id})
		}
		raw, err := json.Marshal(details)
		if err != nil {
			return "", err
		}
		q.Set("authorization_details", string(raw))
	}
	return meta.AuthorizationEndpoint + "?" + q.Encode(), nil
}

// Exchange redeems code for an access token for resource and returns the BSN
// it carries, after checking the token as its resource server.
func (c *Client) Exchange(ctx context.Context, provider, resource, code, verifier string) (string, error) {
	p, ok := c.providers[provider]
	if !ok {
		return "", fmt.Errorf("provider %q is not a client of the authorization server", provider)
	}
	c.mu.Lock()
	reg, ok := c.regs[provider]
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("%w: provider %q has no registration", ErrUnavailable, provider)
	}
	meta, err := c.metadata(ctx)
	if err != nil {
		return "", err
	}
	now := c.now()
	assertion, err := sign(p.Key, map[string]any{"kid": reg.keyID}, "", map[string]any{
		"iss": reg.clientID, "sub": reg.clientID, "aud": meta.Issuer,
		"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(), "jti": randomID(),
	})
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type":            {"authorization_code"},
		"code":                  {code},
		"redirect_uri":          {p.RedirectURI},
		"code_verifier":         {verifier},
		"client_id":             {reg.clientID},
		"client_assertion_type": {clientAssertionType},
		"client_assertion":      {assertion},
		"resource":              {resource},
	}
	proof, err := sign(c.dpop, map[string]any{"jwk": jose.JSONWebKey{Key: &c.dpop.PublicKey}}, "dpop+jwt", map[string]any{
		"jti": randomID(), "htm": http.MethodPost, "htu": meta.TokenEndpoint, "iat": now.Unix(),
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("DPoP", proof)
	status, raw, err := c.do(req)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		var e struct {
			Code string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		switch {
		case e.Code == "invalid_client":
			// The server forgot the client (a restart): the next login
			// registers again.
			c.mu.Lock()
			delete(c.regs, provider)
			c.mu.Unlock()
			return "", fmt.Errorf("%w: the token request was refused with invalid_client", ErrUnavailable)
		case status >= 500:
			return "", fmt.Errorf("%w: token endpoint answered %d", ErrUnavailable, status)
		}
		return "", fmt.Errorf("the token request was refused: %s", e.Code)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" || !strings.EqualFold(out.TokenType, "DPoP") {
		return "", errors.New("the token response carries no DPoP access token")
	}
	keys, err := c.fetchKeys(ctx, meta)
	if err != nil {
		return "", err
	}
	return c.check(out.AccessToken, meta.Issuer, resource, reg.clientID, keys, now)
}

// check validates the access token as its resource server (RFC 9068 section
// 4, RFC 9449 section 7) and returns the BSN.
func (c *Client) check(token, issuer, resource, clientID string, keys jose.JSONWebKeySet, now time.Time) (string, error) {
	tok, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return "", errors.New("the access token is not an ES256 JWT")
	}
	if typ, _ := tok.Headers[0].ExtraHeaders["typ"].(string); !strings.EqualFold(typ, "at+jwt") {
		return "", errors.New("the access token is not of type at+jwt")
	}
	found := keys.Key(tok.Headers[0].KeyID)
	if len(found) != 1 {
		return "", errors.New("the access token is signed with an unknown key")
	}
	var std jwt.Claims
	var claims struct {
		ClientID string `json:"client_id"`
		Scope    string `json:"scope"`
		Cnf      struct {
			JKT string `json:"jkt"`
		} `json:"cnf"`
		BSN any `json:"https://gbo.example/claims/bsn"`
	}
	if err := tok.Claims(found[0].Key, &std, &claims); err != nil {
		return "", errors.New("the access token signature does not verify")
	}
	if err := std.ValidateWithLeeway(jwt.Expected{Issuer: issuer, AnyAudience: jwt.Audience{resource}, Time: now}, leeway); err != nil {
		return "", fmt.Errorf("the access token is not for this issuer: %w", err)
	}
	if std.Expiry == nil {
		return "", errors.New("the access token has no expiry")
	}
	switch {
	case claims.ClientID != clientID:
		return "", errors.New("the access token was issued to another client")
	case !slices.Contains(strings.Fields(claims.Scope), Scope):
		return "", errors.New("the access token is not for issuance")
	case claims.Cnf.JKT == "" || claims.Cnf.JKT != c.jkt:
		return "", errors.New("the access token is not bound to this issuer's DPoP key")
	}
	bsn, _ := claims.BSN.(string)
	if bsn == "" {
		return "", errors.New("the access token carries no BSN")
	}
	return bsn, nil
}

// registration returns the provider's client, registering it when there is
// none or the server's keys changed since.
func (c *Client) registration(ctx context.Context, p Provider, meta serverMetadata, keys string) (registration, error) {
	c.mu.Lock()
	reg, ok := c.regs[p.Name]
	c.mu.Unlock()
	if ok && reg.keys == keys {
		return reg, nil
	}
	now := c.now()
	chain := make([]string, len(p.Chain))
	for i, cert := range p.Chain {
		chain[i] = base64.StdEncoding.EncodeToString(cert.Raw)
	}
	statement, err := sign(p.Key, map[string]any{"x5c": chain}, "", map[string]any{
		"iss": "gbo-demo-credential-issuer", "registryURI": p.RegistryURI,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	if err != nil {
		return registration{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"redirect_uris":              []string{p.RedirectURI},
		"scope":                      Scope,
		"token_endpoint_auth_method": "private_key_jwt",
		"software_statement":         statement,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return registration{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	status, raw, err := c.do(req)
	if err != nil {
		return registration{}, err
	}
	if status != http.StatusCreated {
		var e struct {
			Code        string `json:"error"`
			Description string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e)
		// A refused registration is a fault in the deployment, not something
		// the citizen can wait out; it is reported as unavailable all the
		// same, with the reason in the log.
		return registration{}, fmt.Errorf("%w: registration of %s refused (%d): %s: %s", ErrUnavailable, p.Name, status, e.Code, e.Description)
	}
	var out struct {
		ClientID string `json:"client_id"`
		JWKS     struct {
			Keys []struct {
				KeyID string `json:"kid"`
			} `json:"keys"`
		} `json:"jwks"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ClientID == "" || len(out.JWKS.Keys) == 0 {
		return registration{}, errors.New("the registration response names no client_id or key")
	}
	reg = registration{clientID: out.ClientID, keyID: out.JWKS.Keys[0].KeyID, keys: keys}
	c.mu.Lock()
	c.regs[p.Name] = reg
	c.mu.Unlock()
	return reg, nil
}

// metadata fetches the server's metadata (RFC 8414) and checks its issuer.
func (c *Client) metadata(ctx context.Context) (serverMetadata, error) {
	var meta serverMetadata
	if err := c.getJSON(ctx, c.server+"/.well-known/oauth-authorization-server", &meta); err != nil {
		return serverMetadata{}, err
	}
	if meta.Issuer != c.server {
		return serverMetadata{}, fmt.Errorf("%w: the metadata names issuer %q", ErrUnavailable, meta.Issuer)
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" || meta.JWKSURI == "" || meta.RegistrationEndpoint == "" {
		return serverMetadata{}, fmt.Errorf("%w: the metadata lacks an endpoint", ErrUnavailable)
	}
	return meta, nil
}

func (c *Client) fetchKeys(ctx context.Context, meta serverMetadata) (jose.JSONWebKeySet, error) {
	var set jose.JSONWebKeySet
	if err := c.getJSON(ctx, meta.JWKSURI, &set); err != nil {
		return jose.JSONWebKeySet{}, err
	}
	return set, nil
}

// keys returns the server's key ids, sorted, as one string.
func (c *Client) keys(ctx context.Context, meta serverMetadata) (string, error) {
	set, err := c.fetchKeys(ctx, meta)
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, len(set.Keys))
	for _, k := range set.Keys {
		ids = append(ids, k.KeyID)
	}
	sort.Strings(ids)
	return strings.Join(ids, " "), nil
}

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
		return fmt.Errorf("%w: GET %s answered %d", ErrUnavailable, target, status)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: GET %s: %v", ErrUnavailable, target, err)
	}
	return nil
}

func (c *Client) do(req *http.Request) (int, []byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return resp.StatusCode, raw, nil
}

func sign(key crypto.Signer, headers map[string]any, typ string, claims map[string]any) (string, error) {
	opts := &jose.SignerOptions{}
	for k, v := range headers {
		opts = opts.WithHeader(jose.HeaderKey(k), v)
	}
	if typ != "" {
		opts = opts.WithType(jose.ContentType(typ))
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
