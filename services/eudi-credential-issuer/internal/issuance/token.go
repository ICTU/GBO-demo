package issuance

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"gbo-demo/oid4vc-kit/clientattest"
	"gbo-demo/oid4vc-kit/dpop"
	"gbo-demo/oid4vc-kit/oid4vci"
)

// Challenge returns a fresh attestation challenge for the client
// attestation PoP; it is accepted once, within the nonce lifetime.
func (s *Service) Challenge(ctx context.Context, t Target) (string, error) {
	if _, _, err := s.resolve(t); err != nil {
		return "", err
	}
	c, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := s.onetime.Put(ctx, scoped(kindChallenge, s.Identifier(t)), c, s.now().Add(s.cfg.NonceTTL)); err != nil {
		return "", err
	}
	return c, nil
}

// TokenRequest is a token request with the headers that authenticate it.
type TokenRequest struct {
	Target            Target
	GrantType         string
	PreAuthorizedCode string
	TxCode            string
	// Code, CodeVerifier and RedirectURI are the authorization code grant.
	Code         string
	CodeVerifier string
	RedirectURI  string
	// RefreshToken is the refresh token grant.
	RefreshToken string
	// ClientID is the client_id form parameter, if sent.
	ClientID string
	// ClientAttestation and ClientAttestationPoP are the WIA headers.
	ClientAttestation    string
	ClientAttestationPoP string
	DPoP                 []string
}

// Token redeems a pre-authorized code, or an authorization code on a
// profile that offers the authorization code flow, for a DPoP-bound access
// token. The wallet must authenticate with a Wallet Instance Attestation of
// a provider trusted for this profile (HAIP 1.0 section 4.4). Client
// authentication and DPoP are checked before the code is used, so a failed
// request does not burn the code.
func (s *Service) Token(ctx context.Context, req TokenRequest) (oid4vci.TokenResponse, error) {
	_, p, err := s.resolve(req.Target)
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	code, err := s.grantCode(p, req)
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	now := s.now()
	id := s.Identifier(req.Target)
	att, err := s.authenticateWallet(ctx, req.Target, p, req.ClientAttestation, req.ClientAttestationPoP, req.ClientID, now)
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}

	// htu is checked against the public URL: the issuer runs behind a
	// proxy, so the request URL it sees is not the one the wallet used.
	proof, err := dpop.Verify(req.DPoP, dpop.Expected{Method: "POST", URL: id + "/token"}, now)
	if err != nil {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidDPoPProof, err.Error())
	}
	if ok, err := s.onetime.FirstUse(ctx, kindDPoP, proof.JTI, proof.IAT.Add(dpop.Window), now); err != nil {
		return oid4vci.TokenResponse{}, err
	} else if !ok {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidDPoPProof, "the DPoP proof has been used before")
	}

	if req.GrantType == oid4vci.GrantRefreshToken {
		return s.refresh(ctx, req, att, proof.JKT, now)
	}
	session, err := s.sessions.RedeemCode(ctx, req.Target.Provider, p.Name, hash(code), txCodeHash(code, req.TxCode), now)
	switch {
	case errors.Is(err, ErrNotFound):
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the code is unknown, used or expired")
	case errors.Is(err, ErrTxCodeMissing):
		// OpenID4VCI 1.0 section 6.3: a malformed request, no failed attempt.
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidRequest, "the offer asks for a transaction code")
	case errors.Is(err, ErrTxCodeMismatch):
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the transaction code is wrong")
	case errors.Is(err, ErrTxCodeUnexpected):
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidRequest, "tx_code is not expected for this offer")
	}
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	// A code is redeemed with its own grant only, and an authorization code
	// only as its pushed request allows.
	b := session.Binding
	switch {
	case req.GrantType == oid4vci.GrantPreAuthorizedCode && b != nil,
		req.GrantType == GrantAuthorizationCode && b == nil:
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the code is unknown, used or expired")
	case b != nil && b.ClientID != att.ClientID:
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the code was issued to another client")
	case b != nil && b.RedirectURI != req.RedirectURI:
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "redirect_uri does not match the authorization request")
	case b != nil && !pkceMatches(b.CodeChallenge, req.CodeVerifier):
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "code_verifier does not match the code_challenge")
	case b != nil && b.DPoPJKT != "" && b.DPoPJKT != proof.JKT:
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the code is bound to another DPoP key")
	}
	if revoked, err := s.referenceRevoked(ctx, session); err != nil {
		return oid4vci.TokenResponse{}, err
	} else if revoked {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the offer has been revoked")
	}

	return s.grant(ctx, req.Target, p, session, att, proof.JKT, now, nil, "")
}

// grantCode checks the parameters of the request's grant on profile p and
// returns the code it redeems: the pre-authorized or authorization code, or
// none for a refresh token.
func (s *Service) grantCode(p Profile, req TokenRequest) (string, error) {
	switch req.GrantType {
	case oid4vci.GrantPreAuthorizedCode:
		if req.PreAuthorizedCode == "" {
			return "", refuse(oid4vci.ErrInvalidRequest, "pre-authorized_code is required")
		}
		return req.PreAuthorizedCode, nil
	case GrantAuthorizationCode:
		if !s.offersLogin(p) {
			return "", refuse(oid4vci.ErrUnsupportedGrantType, "only the pre-authorized code grant is supported")
		}
		if req.Code == "" || req.TxCode != "" {
			return "", refuse(oid4vci.ErrInvalidRequest, "code is required and tx_code is not expected")
		}
		return req.Code, nil
	case oid4vci.GrantRefreshToken:
		if p.Kind != KindEUDI {
			return "", refuse(oid4vci.ErrUnsupportedGrantType, "refresh tokens are not offered on this profile")
		}
		if req.RefreshToken == "" || req.TxCode != "" {
			return "", refuse(oid4vci.ErrInvalidRequest, "refresh_token is required and tx_code is not expected")
		}
		return "", nil
	default:
		return "", refuse(oid4vci.ErrUnsupportedGrantType, "only the pre-authorized code, authorization code and refresh token grants are supported")
	}
}

// refreshGrant is what the issuer keeps of a refresh token: the session whose
// claims it re-issues, the wallet it was issued to and its DPoP key.
type refreshGrant struct {
	SessionID string `json:"session_id"`
	Provider  string `json:"provider"`
	Profile   string `json:"profile"`
	ClientID  string `json:"client_id"`
	JKT       string `json:"jkt"`
}

const pendingRefresh = "refresh"

// grant issues an access token for session, and on the eudi profile a
// refresh token; the session's claims are then kept until the refresh token
// expires. A refresh passes its refresh token and its expiry: it is not
// rotated (FAPI 2.0 section 5.3.2.1).
func (s *Service) grant(ctx context.Context, t Target, p Profile, session Session, att clientattest.Verified, jkt string, now time.Time, refreshing *PendingAuthorization, refreshToken string) (oid4vci.TokenResponse, error) {
	token, err := randomToken()
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	if err := s.sessions.SaveToken(ctx, AccessToken{
		Hash: hash(token), SessionID: session.ID, JKT: jkt, ClientID: att.ClientID,
		WalletProvider: att.Anchor, ExpiresAt: now.Add(s.cfg.TokenTTL),
	}); err != nil {
		return oid4vci.TokenResponse{}, err
	}
	resp := oid4vci.TokenResponse{
		AccessToken:          token,
		TokenType:            "DPoP",
		ExpiresIn:            int64(s.cfg.TokenTTL.Seconds()),
		AuthorizationDetails: authorizationDetails(session),
	}
	if p.Kind != KindEUDI {
		return resp, nil
	}
	if refreshing != nil {
		resp.RefreshToken = refreshToken
		return resp, s.sessions.ExtendSession(ctx, session.ID, refreshing.ExpiresAt)
	}
	if resp.RefreshToken, err = randomToken(); err != nil {
		return oid4vci.TokenResponse{}, err
	}
	until := now.Add(s.cfg.RefreshTTL)
	if err := s.putPending(ctx, pendingRefresh, resp.RefreshToken, refreshGrant{
		SessionID: session.ID, Provider: t.Provider, Profile: p.Name, ClientID: att.ClientID, JKT: jkt,
	}, until); err != nil {
		return oid4vci.TokenResponse{}, err
	}
	if err := s.sessions.ExtendSession(ctx, session.ID, until); err != nil {
		return oid4vci.TokenResponse{}, err
	}
	return resp, nil
}

// refresh redeems a refresh token: until it expires, by the wallet and DPoP
// key it was issued to, for an offer not revoked. The new session holds the
// claims of the first issuance under new credential identifiers (R2): the
// source is not asked again.
func (s *Service) refresh(ctx context.Context, req TokenRequest, att clientattest.Verified, jkt string, now time.Time) (oid4vci.TokenResponse, error) {
	_, p, err := s.resolve(req.Target)
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	rec, err := s.sessions.Pending(ctx, pendingRefresh, hash(req.RefreshToken), now)
	if errors.Is(err, ErrNotFound) {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the refresh token is unknown or expired")
	}
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	var g refreshGrant
	if err := json.Unmarshal(rec.Payload, &g); err != nil {
		return oid4vci.TokenResponse{}, err
	}
	switch {
	case g.Provider != req.Target.Provider || g.Profile != p.Name:
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the refresh token is unknown or expired")
	case g.ClientID != att.ClientID:
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the refresh token was issued to another wallet")
	case g.JKT != jkt:
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the refresh token is bound to another DPoP key")
	}
	old, err := s.sessions.Session(ctx, g.SessionID)
	if errors.Is(err, ErrNotFound) {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the refresh token is unknown or expired")
	}
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	if revoked, err := s.referenceRevoked(ctx, old); err != nil {
		return oid4vci.TokenResponse{}, err
	} else if revoked {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the offer has been revoked")
	}
	// A re-issuance has the claims of the first issuance (R2); a session
	// whose source has not answered yet has none.
	if old.Pending != nil {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the first issuance has not finished")
	}
	// A session of its own: the credentials of the first issuance stay
	// issued, these are new. Its code is random and handed to nobody.
	next := Session{Provider: old.Provider, Profile: old.Profile, Reference: old.Reference, ExpiresAt: rec.ExpiresAt}
	var code string
	if next.ID, err = randomToken(); err != nil {
		return oid4vci.TokenResponse{}, err
	}
	if code, err = randomToken(); err != nil {
		return oid4vci.TokenResponse{}, err
	}
	next.CodeHash = hash(code)
	for _, d := range old.Documents {
		if d.CredentialID, err = randomToken(); err != nil {
			return oid4vci.TokenResponse{}, err
		}
		next.Documents = append(next.Documents, d)
	}
	if err := s.sessions.CreateSession(ctx, next); err != nil {
		return oid4vci.TokenResponse{}, err
	}
	return s.grant(ctx, req.Target, p, next, att, jkt, now, &rec, req.RefreshToken)
}

// authenticateWallet checks the Wallet Instance Attestation and its PoP,
// with a fresh attestation challenge, for the token and PAR endpoints.
// clientID, when sent, must be the attested one.
func (s *Service) authenticateWallet(ctx context.Context, t Target, p Profile, attestation, pop, clientID string, now time.Time) (clientattest.Verified, error) {
	id := s.Identifier(t)
	if attestation == "" || pop == "" {
		return clientattest.Verified{}, refuse(oid4vci.ErrInvalidClientAttestation, "client attestation and PoP are required")
	}
	att, err := clientattest.Verify(attestation, pop, p.WalletProviders, id, now)
	if errors.Is(err, clientattest.ErrExpired) {
		return clientattest.Verified{}, refuse(oid4vci.ErrUseFreshAttestation, err.Error())
	}
	if err != nil {
		return clientattest.Verified{}, refuse(oid4vci.ErrInvalidClientAttestation, err.Error())
	}
	if clientID != "" && clientID != att.ClientID {
		return clientattest.Verified{}, refuse(oid4vci.ErrInvalidClientAttestation, "client_id does not match the client attestation")
	}
	// Without a valid challenge the client gets a fresh one with
	// use_attestation_challenge (draft-ietf-oauth-attestation-based-client-auth
	// -07 section 6.2, -12 section 6.1).
	if att.Challenge == "" {
		return clientattest.Verified{}, s.useChallenge(ctx, t, "the attestation PoP must carry a challenge")
	}
	if ok, err := s.onetime.Take(ctx, scoped(kindChallenge, id), att.Challenge, now); err != nil {
		return clientattest.Verified{}, err
	} else if !ok {
		return clientattest.Verified{}, s.useChallenge(ctx, t, "the attestation challenge is unknown, expired or used")
	}
	if ok, err := s.onetime.FirstUse(ctx, kindPoP, att.PoPID, att.PoPExpiry, now); err != nil {
		return clientattest.Verified{}, err
	} else if !ok {
		return clientattest.Verified{}, refuse(oid4vci.ErrInvalidClientAttestation, "the attestation PoP has been used before")
	}
	return att, nil
}

// useChallenge returns the use_attestation_challenge refusal with a fresh
// challenge.
func (s *Service) useChallenge(ctx context.Context, t Target, description string) error {
	c, err := s.Challenge(ctx, t)
	if err != nil {
		return err
	}
	return &Error{Code: oid4vci.ErrUseAttestationChallenge, Description: description, Challenge: c}
}

// authorizationDetails lists, per credential configuration, the
// credential identifiers the wallet may ask for.
func authorizationDetails(s Session) []oid4vci.AuthorizationDetail {
	var out []oid4vci.AuthorizationDetail
	index := map[string]int{}
	for _, d := range s.Documents {
		i, ok := index[d.ConfigurationID]
		if !ok {
			i = len(out)
			index[d.ConfigurationID] = i
			out = append(out, oid4vci.AuthorizationDetail{
				Type: oid4vci.AuthorizationDetailsType, CredentialConfigurationID: d.ConfigurationID,
			})
		}
		out[i].CredentialIdentifiers = append(out[i].CredentialIdentifiers, d.CredentialID)
	}
	return out
}
