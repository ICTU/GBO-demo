package issuance

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/sdjwtvc"
)

// Disclosure-based issuance: the wallet discloses the BSN from its PID
// (OpenID4VP 1.0), the issuer fetches the documents for that BSN from the
// source and answers with a redirect_uri that carries a pre-authorized
// Credential Offer, which the wallet then redeems as usual.

// PIDType is the type of the PID the issuer asks for: the NL PID, which
// the NL Wallet holds and the test PID copies.
const PIDType = "urn:eudi:pid:nl:1"

// pidQueryID is the id of the DCQL credential query for the PID.
const pidQueryID = "pid"

// pidQuery asks for the BSN of the PID, and nothing else.
var pidQuery = oid4vp.Query{Credentials: []oid4vp.CredentialQuery{{
	ID: pidQueryID, Format: sdjwtvc.Type,
	Meta:   oid4vp.Meta{VCTValues: []string{PIDType}},
	Claims: []oid4vp.ClaimQuery{{Path: []string{"bsn"}}},
}}}

// ErrUnknownOffer means no disclosure offer has the key.
var ErrUnknownOffer = errors.New("unknown disclosure offer")

// DisclosureResponsePath is the path below an issuer identifier where the
// wallet posts its response; the session id follows it.
const DisclosureResponsePath = "/vp/response/"

// DisclosureRequest starts a disclosure session for the offer and returns
// the signed request object the wallet fetches from request_uri. Each
// fetch starts a new session.
func (s *Service) DisclosureRequest(ctx context.Context, t Target, offerKey string) (string, error) {
	prov, p, err := s.resolve(t)
	if err != nil {
		return "", err
	}
	o, ok := s.disclosureOffers[t.Provider+"/"+offerKey]
	// Disclosure-based issuance runs on the nl and eudi profiles only: the
	// wallets read the registration certificate in verifier_info differently.
	if !ok || prov.requestSigner == nil || p.Kind == KindCombined {
		return "", ErrUnknownOffer
	}
	key, err := oid4vp.NewResponseKey()
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key.Key)
	if err != nil {
		return "", err
	}
	var id, nonce, state string
	for _, v := range []*string{&id, &nonce, &state} {
		if *v, err = randomToken(); err != nil {
			return "", err
		}
	}
	now := s.now()
	expires := now.Add(s.cfg.DisclosureTTL)
	if err := s.sessions.CreateDisclosure(ctx, DisclosureSession{
		ID: id, Provider: t.Provider, Profile: p.Name, Offer: o.Key, Nonce: nonce, State: state,
		ResponseKeyID: key.KeyID, ResponseKey: der, ExpiresAt: expires,
	}); err != nil {
		return "", err
	}
	// The NL Wallet base64url-decodes the WRPRC in verifier_info; the EUDI
	// wallets read the JWT as is.
	encoding := oid4vp.RegistrationJWT
	if p.Kind == KindNL {
		encoding = oid4vp.RegistrationBase64URL
	}
	return prov.requestSigner.Sign(oid4vp.Request{
		Session: oid4vp.Session{
			ClientID: prov.requestSigner.ClientID(), Nonce: nonce, State: state, Query: pidQuery, Key: key,
		},
		ResponseURI:             s.Identifier(t) + DisclosureResponsePath + id,
		RegistrationCertificate: string(prov.VerifierRegistrationCertificate),
		Encoding:                encoding,
		IssuedAt:                now,
		Expires:                 expires,
	})
}

// DisclosureForm is what a wallet posts to the response_uri: the
// encrypted response, or an error when the user declined or the wallet
// failed (OpenID4VP 1.0 section 8.5).
type DisclosureForm struct {
	Response string
	Error    string
	State    string
}

// DisclosureResponse ends the session and handles the wallet's answer. For
// a verified PID it fetches the documents for its BSN and returns the
// redirect_uri with the Credential Offer. A wallet error ends the session
// without one: the redirect_uri is empty and the error nil.
//
// Refusals: an unknown, expired or used session and a response the
// verifier refuses are invalid_request; a source without data is
// ErrNoData; an unreachable source or PID status list and unusable source
// data are server errors.
func (s *Service) DisclosureResponse(ctx context.Context, t Target, sessionID string, f DisclosureForm) (string, error) {
	prov, p, err := s.resolve(t)
	if err != nil {
		return "", err
	}
	now := s.now()
	d, err := s.sessions.TakeDisclosure(ctx, sessionID, now)
	if errors.Is(err, ErrNotFound) || (err == nil && (d.Provider != t.Provider || d.Profile != p.Name)) {
		return "", refuse(oid4vci.ErrInvalidRequest, "unknown, expired or used session")
	}
	if err != nil {
		return "", err
	}
	// The session may come from a replica with another configuration.
	o, ok := s.disclosureOffers[d.Provider+"/"+d.Offer]
	if !ok || prov.requestSigner == nil {
		return "", fmt.Errorf("disclosure offer %q of the session is not configured here", d.Offer)
	}
	if f.Error != "" {
		// The wallet echoes state with its error (OpenID4VP 1.0 section
		// 8.5); the session has ended either way.
		if f.State != d.State {
			return "", refuse(oid4vci.ErrInvalidRequest, "the error does not carry the session's state")
		}
		return "", nil
	}
	key, err := x509.ParsePKCS8PrivateKey(d.ResponseKey)
	if err != nil {
		return "", err
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("the session's response key is not an EC key")
	}
	disclosure, err := s.verifier.VerifyResponse(ctx, f.Response, oid4vp.Session{
		ClientID: prov.requestSigner.ClientID(), Nonce: d.Nonce, State: d.State, Query: pidQuery,
		Key: oid4vp.ResponseKey{KeyID: d.ResponseKeyID, Key: ecKey},
	}, now)
	if errors.Is(err, oid4vp.ErrStatusUnavailable) {
		return "", err
	}
	if err != nil {
		return "", refuse(oid4vci.ErrInvalidRequest, err.Error())
	}
	bsn, _ := disclosure.Claims["bsn"].(string)
	if bsn == "" {
		return "", refuse(oid4vci.ErrInvalidRequest, "the PID has no BSN")
	}
	pid := DisclosedPID{
		QueryID: pidQueryID, AttestationType: PIDType, Attributes: map[string]any{"bsn": bsn},
		CA: disclosure.Issuer.Issuer.CommonName, Signed: claimTime(disclosure.Claims, "iat"),
		ValidFrom: claimTime(disclosure.Claims, "nbf"), ValidUntil: claimTime(disclosure.Claims, "exp"),
	}
	offer, err := s.createOffer(ctx, t, p, o.ConfigurationIDs, "", s.cfg.DisclosureTTL, func(id string) ([]SourceDocument, error) {
		return s.source.FetchDisclosed(ctx, id, pid, o.Parameters)
	})
	if errors.Is(err, ErrNoData) && p.Kind == KindEUDI {
		return "", &NoDataError{Page: s.noDataPage(t, o.Key)}
	}
	if err != nil {
		return "", err
	}
	return offer.Links["openid-credential-offer"], nil
}

// NoDataError refuses a disclosure on a KindEUDI profile whose source has
// no data. The EUDI wallets show a refusal as a generic error, so the
// wallet is sent to Page, which says so; the NL Wallet has its own screen
// for no_issuable_attestations.
type NoDataError struct {
	Page string
}

func (e *NoDataError) Error() string { return ErrNoData.Error() }

func (e *NoDataError) Unwrap() error { return ErrNoData }

// noDataPage is the page a wallet is sent to after a disclosure the source
// has no data for. It names the offer only, not the citizen.
func (s *Service) noDataPage(t Target, offerKey string) string {
	return s.Identifier(t) + "/vp/" + offerKey + "/no-data"
}

// NoData is what the no-data page of an offer shows: the organization of
// the provider and the name of the attestation.
type NoData struct {
	Organization string
	Attestation  string
}

// NoDataPage describes the no-data page of an offer on a KindEUDI
// profile, or returns ErrUnknownOffer.
func (s *Service) NoDataPage(t Target, offerKey string) (NoData, error) {
	prov, p, err := s.resolve(t)
	if err != nil {
		return NoData{}, err
	}
	o, ok := s.disclosureOffers[t.Provider+"/"+offerKey]
	if !ok || p.Kind != KindEUDI {
		return NoData{}, ErrUnknownOffer
	}
	return NoData{Organization: issuerName(prov.AccessChain[0]), Attestation: displayName(s.configs[o.ConfigurationIDs[0]].Display)}, nil
}

// displayName prefers the Dutch name of a credential.
func displayName(display []Display) string {
	for _, d := range display {
		if d.Locale == "nl" {
			return d.Name
		}
	}
	if len(display) > 0 {
		return display[0].Name
	}
	return ""
}

// claimTime reads a NumericDate claim; zero when it is absent.
func claimTime(claims map[string]any, name string) time.Time {
	if f, ok := claims[name].(float64); ok {
		return time.Unix(int64(f), 0).UTC()
	}
	return time.Time{}
}

// DisclosureLink is how a wallet of one profile starts a disclosure offer:
// its client_id, its request_uri and the link that carries both.
type DisclosureLink struct {
	ClientID   string `json:"client_id"`
	RequestURI string `json:"request_uri"`
	Link       string `json:"link"`
}

// DisclosureOfferLinks are the fixed links of one disclosure offer, per
// profile.
type DisclosureOfferLinks struct {
	Provider string                    `json:"provider"`
	Key      string                    `json:"key"`
	Profiles map[string]DisclosureLink `json:"profiles"`
}

// DisclosureOfferLinks lists every disclosure offer with its fixed link per
// profile, sorted by provider and key: openid4vp:// for KindEUDI, the
// wallet's universal link for KindNL when its base is configured. The
// links are public; a frontend shows them as QR codes.
func (s *Service) DisclosureOfferLinks() []DisclosureOfferLinks {
	out := make([]DisclosureOfferLinks, 0, len(s.disclosureOffers))
	for _, o := range s.disclosureOffers {
		prov := s.providers[o.Provider]
		entry := DisclosureOfferLinks{Provider: o.Provider, Key: o.Key, Profiles: map[string]DisclosureLink{}}
		for _, p := range s.profiles {
			requestURI := s.Identifier(Target{Provider: o.Provider, Profile: p.Name}) + "/vp/" + o.Key + "/request"
			query := url.Values{"client_id": {prov.requestSigner.ClientID()}, "request_uri": {requestURI}}.Encode()
			var link string
			switch {
			case p.Kind == KindEUDI:
				link = "openid4vp://?" + query
			case p.Kind == KindNL && p.UniversalLinkBase != "":
				link = strings.TrimRight(p.UniversalLinkBase, "/") + "/disclosure_based_issuance?" + query
			default:
				continue
			}
			entry.Profiles[p.Name] = DisclosureLink{ClientID: prov.requestSigner.ClientID(), RequestURI: requestURI, Link: link}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Key < out[j].Key
	})
	return out
}
