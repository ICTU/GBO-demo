package authorization

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Dynamic client registration with a sealed software statement (ETSI
// TS 119 478 clause 6.1.3.2, REQ-AZSP-6.1.3.2-01; RFC 7591 section 3.1.1).
// The QTSP signs the software statement itself with the key of its X.509
// certificate (REQ-TSP-6.1.3.2-01); the statement names its entry in the
// register of wallet-relying parties (registryURI), where the authorization
// server looks up its entitlement (figure 2).

// QEAAProviderEntitlement is the entitlement of a qualified trust service
// provider issuing qualified electronic attestations of attributes
// (CIR (EU) 2025/848 Annex I point 12, in the URI form of ETSI/EUDI TS5).
// It is the only entitlement that grants registration: Article 45e
// concerns these providers.
const QEAAProviderEntitlement = "https://uri.etsi.org/19475/Entitlement/QEAA_Provider"

// softwareStatementLeeway allows for clock skew on iat and exp.
const softwareStatementLeeway = 30 * time.Second

// oidOrganizationIdentifier is the X.520 organizationIdentifier attribute
// (2.5.4.97): the identifier of the organisation in the certificate.
var oidOrganizationIdentifier = asn1.ObjectIdentifier{2, 5, 4, 97}

// RegisterEntry is the entry of a wallet-relying party in the register.
type RegisterEntry struct {
	Identifiers  []string
	LegalName    string
	Entitlements []string
}

// ErrNotRegistered: the register has no entry for the registryURI.
var ErrNotRegistered = errors.New("no entry in the register")

// ErrRegisterUnavailable: the register could not be consulted.
var ErrRegisterUnavailable = errors.New("the register is unavailable")

// Register is the register of wallet-relying parties (CIR (EU) 2025/848),
// looked up by the registryURI of an entry.
type Register interface {
	Lookup(ctx context.Context, registryURI string) (RegisterEntry, error)
}

// RegistrationRequest is a client registration request (RFC 7591
// section 3.1): the plain JSON metadata and the software statement.
type RegistrationRequest struct {
	Metadata          map[string]json.RawMessage
	SoftwareStatement string
}

// RegisteredClient is the client information response (RFC 7591
// section 3.2.1).
type RegisteredClient struct {
	ClientID                string
	ClientIDIssuedAt        time.Time
	RedirectURIs            []string
	Scope                   string
	TokenEndpointAuthMethod string
	JWKS                    jose.JSONWebKeySet
}

// registeredClient is a client registered here, with what re-verification
// needs.
type registeredClient struct {
	client         *Client
	registryURI    string
	organizationID string
	active         bool
}

type registeredClients struct {
	mu      sync.Mutex
	clients map[string]*registeredClient
}

func (r *registeredClients) add(c *registeredClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients[c.client.ID] = c
}

func (r *registeredClients) active(id string) (*Client, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clients[id]
	if !ok || !c.active {
		return nil, false
	}
	return c.client, true
}

func (r *registeredClients) snapshot() []*registeredClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*registeredClient
	for _, c := range r.clients {
		if c.active {
			out = append(out, c)
		}
	}
	return out
}

func (r *registeredClients) deactivate(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[id]; ok {
		c.active = false
	}
}

// client returns a usable client: from configuration, or registered here and
// still active.
func (s *Service) client(id string) (*Client, bool) {
	if c, ok := s.cfg.Clients.Client(id); ok {
		return c, true
	}
	return s.registered.active(id)
}

// Register registers a client.
func (s *Service) Register(ctx context.Context, req RegistrationRequest) (RegisteredClient, *Error) {
	now := s.cfg.Now()
	statement, leaf, err := s.verifySoftwareStatement(req.SoftwareStatement, now)
	if err != nil {
		return RegisteredClient{}, &Error{Code: "invalid_software_statement", Description: err.Error()}
	}
	organizationID := organizationIdentifier(leaf)
	if oauthErr := s.approve(ctx, statement.RegistryURI, organizationID); oauthErr != nil {
		return RegisteredClient{}, oauthErr
	}
	// Metadata in the software statement take precedence over plain JSON
	// members (RFC 7591 section 3.1.1).
	metadata := map[string]json.RawMessage{}
	for k, v := range req.Metadata {
		metadata[k] = v
	}
	for k, v := range statement.Metadata {
		metadata[k] = v
	}
	m, oauthErr := parseClientMetadata(metadata)
	if oauthErr != nil {
		return RegisteredClient{}, oauthErr
	}
	key := jose.JSONWebKey{Key: leaf.PublicKey, Use: "sig"}
	thumbprint, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		return RegisteredClient{}, &Error{Code: "invalid_software_statement", Description: "the certificate key is not usable"}
	}
	key.KeyID = base64.RawURLEncoding.EncodeToString(thumbprint)
	c := &Client{ID: RandomID(), RedirectURIs: m.redirectURIs, Scopes: m.scopes, Keys: jose.JSONWebKeySet{Keys: []jose.JSONWebKey{key}}}
	s.registered.add(&registeredClient{client: c, registryURI: statement.RegistryURI, organizationID: organizationID, active: true})
	return RegisteredClient{
		ClientID:                c.ID,
		ClientIDIssuedAt:        now,
		RedirectURIs:            m.redirectURIs,
		Scope:                   m.scope,
		TokenEndpointAuthMethod: "private_key_jwt",
		JWKS:                    c.Keys,
	}, nil
}

// approve looks up the entry in the register: it must exist, carry the
// QEAA_Provider entitlement, and name the organisation of the certificate.
func (s *Service) approve(ctx context.Context, registryURI, organizationID string) *Error {
	if s.cfg.Register == nil {
		return &Error{Code: "temporarily_unavailable", Description: "no register is configured"}
	}
	entry, err := s.cfg.Register.Lookup(ctx, registryURI)
	switch {
	case errors.Is(err, ErrRegisterUnavailable):
		return &Error{Code: "temporarily_unavailable", Description: "the register cannot be consulted"}
	case err != nil:
		return &Error{Code: "unapproved_software_statement", Description: "registryURI is not in the register"}
	case !slices.Contains(entry.Entitlements, QEAAProviderEntitlement):
		return &Error{Code: "unapproved_software_statement", Description: "the register entry has no QEAA_Provider entitlement"}
	case !slices.Contains(entry.Identifiers, organizationID):
		return &Error{Code: "unapproved_software_statement", Description: "the organizationIdentifier of the certificate is not an identifier of the register entry"}
	}
	return nil
}

// RecheckResult reports a re-verification run.
type RecheckResult struct {
	Checked, Deactivated, Unavailable int
}

// RecheckEntitlements verifies the entitlement of every active registered
// client again (ETSI TS 119 478 clause 6.1.3.2, figure 2, step 2). A client
// that is no longer approved is deactivated. When the register cannot be
// consulted the client stays active until the next check: a demo choice,
// to be reassessed for production.
func (s *Service) RecheckEntitlements(ctx context.Context) RecheckResult {
	var res RecheckResult
	for _, c := range s.registered.snapshot() {
		res.Checked++
		oauthErr := s.approve(ctx, c.registryURI, c.organizationID)
		switch {
		case oauthErr == nil:
		case oauthErr.Code == "temporarily_unavailable":
			res.Unavailable++
		default:
			s.registered.deactivate(c.client.ID)
			res.Deactivated++
		}
	}
	return res
}

// --- the software statement ------------------------------------------------------

type softwareStatement struct {
	RegistryURI string
	Metadata    map[string]json.RawMessage
}

// verifySoftwareStatement checks the signature against the leaf certificate
// in x5c, the chain against the trust anchors, the certificate profile and
// the required claims, and returns the statement and the leaf certificate.
func (s *Service) verifySoftwareStatement(raw string, now time.Time) (softwareStatement, *x509.Certificate, error) {
	if raw == "" {
		return softwareStatement{}, nil, errors.New("software_statement is required")
	}
	if s.cfg.TrustAnchors == nil {
		return softwareStatement{}, nil, errors.New("no trust anchors are configured")
	}
	tok, err := jwt.ParseSigned(raw, clientAssertionAlgorithms)
	if err != nil {
		return softwareStatement{}, nil, errors.New("software_statement is not a JWT signed with ES256 or PS256")
	}
	// Certificates parses x5c and verifies the chain, the leaf first.
	chains, err := tok.Headers[0].Certificates(x509.VerifyOptions{
		Roots: s.cfg.TrustAnchors, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return softwareStatement{}, nil, errors.New("x5c is missing, or the certificate does not chain to a trust anchor or is not valid now")
	}
	leaf := chains[0][0]
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return softwareStatement{}, nil, errors.New("the certificate does not allow digital signatures")
	}
	if organizationIdentifier(leaf) == "" {
		return softwareStatement{}, nil, errors.New("the certificate has no organizationIdentifier")
	}
	var claims jwt.Claims
	var all map[string]json.RawMessage
	if err := tok.Claims(leaf.PublicKey, &claims, &all); err != nil {
		return softwareStatement{}, nil, errors.New("software_statement signature does not verify with the certificate")
	}
	var registryURI string
	if raw, ok := all["registryURI"]; ok {
		_ = json.Unmarshal(raw, &registryURI)
	}
	if claims.Issuer == "" || registryURI == "" || claims.IssuedAt == nil || claims.Expiry == nil {
		return softwareStatement{}, nil, errors.New("software_statement must contain iss, registryURI, iat and exp")
	}
	if err := claims.ValidateWithLeeway(jwt.Expected{Time: now}, softwareStatementLeeway); err != nil {
		return softwareStatement{}, nil, errors.New("software_statement is expired or issued in the future")
	}
	metadata := map[string]json.RawMessage{}
	for _, name := range clientMetadataNames {
		if v, ok := all[name]; ok {
			metadata[name] = v
		}
	}
	return softwareStatement{RegistryURI: registryURI, Metadata: metadata}, leaf, nil
}

func organizationIdentifier(cert *x509.Certificate) string {
	for _, n := range cert.Subject.Names {
		if n.Type.Equal(oidOrganizationIdentifier) {
			if v, ok := n.Value.(string); ok {
				return v
			}
		}
	}
	return ""
}

// --- client metadata (RFC 7591 section 2) ------------------------------------------

// clientMetadataNames are the metadata this server reads; others are
// ignored, as RFC 7591 section 2 allows.
var clientMetadataNames = []string{"redirect_uris", "scope", "token_endpoint_auth_method", "jwks", "jwks_uri"}

type clientMetadata struct {
	redirectURIs []string
	scope        string
	scopes       map[string]bool
}

func parseClientMetadata(m map[string]json.RawMessage) (clientMetadata, *Error) {
	invalid := func(description string) (clientMetadata, *Error) {
		return clientMetadata{}, &Error{Code: "invalid_client_metadata", Description: description}
	}
	if _, ok := m["jwks"]; ok {
		return invalid("jwks is not accepted: the client key is the key of the certificate")
	}
	if _, ok := m["jwks_uri"]; ok {
		return invalid("jwks_uri is not accepted: the client key is the key of the certificate")
	}
	// Without a value the method is private_key_jwt: the server substitutes
	// it for the RFC 7591 default (section 2), which it does not offer.
	if raw, ok := m["token_endpoint_auth_method"]; ok {
		var method string
		if json.Unmarshal(raw, &method) != nil || method != "private_key_jwt" {
			return invalid("token_endpoint_auth_method must be private_key_jwt")
		}
	}
	var scope string
	if raw, ok := m["scope"]; ok && json.Unmarshal(raw, &scope) != nil {
		return invalid("scope must be a string")
	}
	scopes := map[string]bool{}
	for _, sc := range strings.Fields(scope) {
		if !slices.Contains(OperationScopes, sc) {
			return invalid("scope " + sc + " is not verify or retrieve")
		}
		scopes[sc] = true
	}
	var granted []string
	for _, sc := range OperationScopes {
		if scopes[sc] {
			granted = append(granted, sc)
		}
	}
	if len(granted) == 0 {
		return invalid("scope must contain verify, retrieve or both")
	}
	var redirectURIs []string
	if raw, ok := m["redirect_uris"]; ok && json.Unmarshal(raw, &redirectURIs) != nil {
		return clientMetadata{}, &Error{Code: "invalid_redirect_uri", Description: "redirect_uris must be an array of strings"}
	}
	if len(redirectURIs) == 0 {
		return clientMetadata{}, &Error{Code: "invalid_redirect_uri", Description: "at least one redirect_uri is required"}
	}
	for _, u := range redirectURIs {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Fragment != "" {
			return clientMetadata{}, &Error{Code: "invalid_redirect_uri", Description: "a redirect_uri must be an absolute https URI without fragment: " + u}
		}
	}
	return clientMetadata{redirectURIs: redirectURIs, scope: strings.Join(granted, " "), scopes: scopes}, nil
}
