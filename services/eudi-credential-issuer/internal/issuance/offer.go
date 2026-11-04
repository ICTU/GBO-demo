package issuance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
)

// OfferRequest asks for a Credential Offer with a pre-authorized code. The
// caller (a frontend or service provider backend) has identified the
// subject already.
type OfferRequest struct {
	Profile          string
	ConfigurationIDs []string
	// Subject identifies the person to the source, for example a BSN.
	Subject    map[string]any
	Parameters map[string]string
	// TxCode asks for a transaction code; phase 1 supports none.
	TxCode bool
	// Reference is the caller's own reference for the credentials of this
	// offer; revoking by it revokes all of them. Optional.
	Reference string
}

// Offer is a created Credential Offer with the links that carry it.
type Offer struct {
	CredentialOffer oid4vci.CredentialOffer
	// Links are the ways to hand the offer to a wallet of the profile,
	// keyed by kind ("universal_link", "openid-credential-offer", "haip-vci").
	Links map[string]string
}

// Offer errors that are not protocol refusals.
var (
	ErrUnknownProfile       = errors.New("unknown profile")
	ErrUnknownProvider      = errors.New("unknown provider")
	ErrUnknownConfiguration = errors.New("unknown credential configuration")
	ErrTxCodeUnsupported    = errors.New("a transaction code is not supported for this profile")
	ErrBadSourceData        = errors.New("the source returned data that does not fit the credential configuration")
	ErrMixedProviders       = errors.New("an offer holds credential configurations of one provider only")
)

// CreateOffer fetches the data for the subject from the source and stores
// it in a new session behind a fresh pre-authorized code. Nothing is stored
// when the source has no data or is unavailable; the caller may try again.
func (s *Service) CreateOffer(ctx context.Context, req OfferRequest) (Offer, error) {
	p, ok := s.profiles[req.Profile]
	if !ok {
		return Offer{}, ErrUnknownProfile
	}
	if req.TxCode {
		// The NL Wallet refuses offers with a tx_code; for other wallets it
		// comes in a later phase.
		return Offer{}, ErrTxCodeUnsupported
	}
	ids := uniq(req.ConfigurationIDs)
	if len(ids) == 0 {
		return Offer{}, ErrUnknownConfiguration
	}
	// The provider follows from the configurations; one offer is from one
	// provider, under one issuer identifier.
	var providerName string
	for _, id := range ids {
		c, ok := s.configs[id]
		if !ok {
			return Offer{}, fmt.Errorf("%w: %q", ErrUnknownConfiguration, id)
		}
		if providerName != "" && c.Provider != providerName {
			return Offer{}, ErrMixedProviders
		}
		providerName = c.Provider
	}
	t := Target{Provider: providerName, Profile: p.Name}
	return s.createOffer(ctx, t, p, ids, req.Reference, s.cfg.OfferTTL, func(id string) ([]SourceDocument, error) {
		return s.source.Fetch(ctx, id, req.Subject, req.Parameters)
	})
}

// createOffer fetches the documents of each configuration with fetch and
// stores them in a new session behind a fresh pre-authorized code that
// expires after ttl.
func (s *Service) createOffer(ctx context.Context, t Target, p Profile, ids []string, reference string, ttl time.Duration,
	fetch func(configurationID string) ([]SourceDocument, error)) (Offer, error) {
	code, err := s.createSession(ctx, t, p, ids, reference, ttl, nil, fetch)
	if err != nil {
		return Offer{}, err
	}
	offer := oid4vci.CredentialOffer{
		CredentialIssuer:           s.Identifier(t),
		CredentialConfigurationIDs: ids,
		Grants: map[string]any{
			oid4vci.GrantPreAuthorizedCode: oid4vci.PreAuthorizedCodeGrant{PreAuthorizedCode: code},
		},
	}
	links, err := offerLinks(p, offer)
	if err != nil {
		return Offer{}, err
	}
	return Offer{CredentialOffer: offer, Links: links}, nil
}

// createSession fetches the documents of the configurations ids and stores
// them as a session under a fresh code, valid for ttl, and returns the code.
// binding is set for an authorization code.
func (s *Service) createSession(ctx context.Context, t Target, p Profile, ids []string, reference string, ttl time.Duration,
	binding *CodeBinding, fetch func(configurationID string) ([]SourceDocument, error)) (string, error) {
	now := s.now()
	notBefore := now.UTC().Truncate(24 * time.Hour)
	var docs []Document
	for _, id := range ids {
		c := s.configs[id]
		fetched, err := fetch(id)
		if err != nil {
			return "", err
		}
		if len(fetched) == 0 {
			return "", ErrNoData
		}
		for _, f := range fetched {
			if err := checkSourceDocument(p, c, f); err != nil {
				return "", err
			}
			credentialID, err := randomToken()
			if err != nil {
				return "", err
			}
			docs = append(docs, Document{
				CredentialID:    credentialID,
				ConfigurationID: id,
				Claims:          f.Attributes,
				NotBefore:       notBefore,
				Expires:         notBefore.AddDate(0, 0, c.ValidDays),
			})
		}
	}
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	sessionID, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := s.sessions.CreateSession(ctx, Session{
		ID: sessionID, Provider: t.Provider, Profile: p.Name, CodeHash: hash(code),
		ExpiresAt: now.Add(ttl), Reference: reference, Documents: docs, Binding: binding,
	}); err != nil {
		return "", err
	}
	return code, nil
}

// checkSourceDocument holds a document against its configuration, so that
// what can be offered can also be issued: the type and format must match,
// no attribute may use a name SD-JWT VC reserves, every attribute must be
// described by a claim (the NL Wallet refuses undescribed attributes) and
// every mandatory claim must be present. For KindNL every number must be a
// whole number: the NL Wallet knows no others.
func checkSourceDocument(p Profile, c CredentialConfiguration, d SourceDocument) error {
	if d.AttestationType != c.VCT {
		return fmt.Errorf("%w: type %q, want %q", ErrBadSourceData, d.AttestationType, c.VCT)
	}
	if d.Format != "" && d.Format != sdjwtvc.Type {
		return fmt.Errorf("%w: format %q", ErrBadSourceData, d.Format)
	}
	if len(d.Attributes) == 0 {
		return fmt.Errorf("%w: no attributes", ErrBadSourceData)
	}
	for name := range d.Attributes {
		if sdjwtvc.Reserved(name) {
			return fmt.Errorf("%w: attribute name %q is reserved", ErrBadSourceData, name)
		}
	}
	if p.servesNL() {
		if err := wholeNumbers(d.Attributes); err != nil {
			return fmt.Errorf("%w: %v", ErrBadSourceData, err)
		}
	}
	described := map[string]bool{}
	for _, cl := range c.Claims {
		described[pathKey(cl.Path)] = true
	}
	for _, leaf := range leafPaths(nil, d.Attributes) {
		if !described[pathKey(leaf)] {
			return fmt.Errorf("%w: attribute %v has no claim metadata", ErrBadSourceData, leaf)
		}
	}
	for _, cl := range c.Claims {
		if cl.Mandatory && !hasPath(d.Attributes, cl.Path) {
			return fmt.Errorf("%w: mandatory attribute %v is missing", ErrBadSourceData, cl.Path)
		}
	}
	return nil
}

// wholeNumbers refuses any number with a fraction or beyond int64, at any
// depth. The error does not repeat the value: it may reach logs.
func wholeNumbers(v any) error {
	switch t := v.(type) {
	case json.Number:
		if _, err := strconv.ParseInt(t.String(), 10, 64); err != nil {
			return errors.New("a number is not a whole number")
		}
	case float64:
		if t != math.Trunc(t) || math.Abs(t) > 1<<53 {
			return errors.New("a number is not a whole number")
		}
	case map[string]any:
		for _, e := range t {
			if err := wholeNumbers(e); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range t {
			if err := wholeNumbers(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func leafPaths(prefix []string, obj map[string]any) [][]string {
	var out [][]string
	for k, v := range obj {
		p := append(append([]string{}, prefix...), k)
		if nested, ok := v.(map[string]any); ok {
			out = append(out, leafPaths(p, nested)...)
			continue
		}
		out = append(out, p)
	}
	return out
}

func hasPath(obj map[string]any, path []string) bool {
	var cur any = obj
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[k]; !ok {
			return false
		}
	}
	return true
}

func pathKey(path []string) string {
	b, _ := json.Marshal(path)
	return string(b)
}

func offerLinks(p Profile, offer oid4vci.CredentialOffer) (map[string]string, error) {
	raw, err := json.Marshal(offer)
	if err != nil {
		return nil, err
	}
	query := url.Values{"credential_offer": {string(raw)}}.Encode()
	links := map[string]string{"openid-credential-offer": "openid-credential-offer://?" + query}
	if p.servesNL() && p.UniversalLinkBase != "" {
		links["universal_link"] = strings.TrimRight(p.UniversalLinkBase, "/") + "/credential-offer?" + query
	}
	if p.servesEUDI() {
		links["haip-vci"] = "haip-vci://?" + query
	}
	return links, nil
}

func uniq(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
