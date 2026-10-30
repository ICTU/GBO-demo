package issuance

import (
	"context"
	"errors"

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
	// ClientID is the client_id form parameter, if sent.
	ClientID string
	// ClientAttestation and ClientAttestationPoP are the WIA headers.
	ClientAttestation    string
	ClientAttestationPoP string
	DPoP                 []string
}

// Token redeems a pre-authorized code for a DPoP-bound access token. The
// wallet must authenticate with a Wallet Instance Attestation of a provider
// trusted for this profile (HAIP 1.0 section 4.4). Client authentication
// and DPoP are checked before the code is used, so a failed request does
// not burn the code.
func (s *Service) Token(ctx context.Context, req TokenRequest) (oid4vci.TokenResponse, error) {
	_, p, err := s.resolve(req.Target)
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	if req.GrantType != oid4vci.GrantPreAuthorizedCode {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrUnsupportedGrantType, "only the pre-authorized code grant is supported")
	}
	if req.PreAuthorizedCode == "" || req.TxCode != "" {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidRequest, "pre-authorized_code is required and tx_code is not expected")
	}
	now := s.now()
	id := s.Identifier(req.Target)

	if req.ClientAttestation == "" || req.ClientAttestationPoP == "" {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidClientAttestation, "client attestation and PoP are required")
	}
	att, err := clientattest.Verify(req.ClientAttestation, req.ClientAttestationPoP, p.WalletProviders, id, now)
	if errors.Is(err, clientattest.ErrExpired) {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrUseFreshAttestation, err.Error())
	}
	if err != nil {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidClientAttestation, err.Error())
	}
	if req.ClientID != "" && req.ClientID != att.ClientID {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidClientAttestation, "client_id does not match the client attestation")
	}
	// Without a valid challenge the client gets a fresh one with
	// use_attestation_challenge (draft-ietf-oauth-attestation-based-client-auth
	// -07 section 6.2, -12 section 6.1).
	if att.Challenge == "" {
		return oid4vci.TokenResponse{}, s.useChallenge(ctx, req.Target, "the attestation PoP must carry a challenge")
	}
	if ok, err := s.onetime.Take(ctx, scoped(kindChallenge, id), att.Challenge, now); err != nil {
		return oid4vci.TokenResponse{}, err
	} else if !ok {
		return oid4vci.TokenResponse{}, s.useChallenge(ctx, req.Target, "the attestation challenge is unknown, expired or used")
	}
	if ok, err := s.onetime.FirstUse(ctx, kindPoP, att.PoPID, att.PoPExpiry, now); err != nil {
		return oid4vci.TokenResponse{}, err
	} else if !ok {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidClientAttestation, "the attestation PoP has been used before")
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

	session, err := s.sessions.RedeemCode(ctx, req.Target.Provider, p.Name, hash(req.PreAuthorizedCode), now)
	if errors.Is(err, ErrNotFound) {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the pre-authorized code is unknown, used or expired")
	}
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	if revoked, err := s.referenceRevoked(ctx, session); err != nil {
		return oid4vci.TokenResponse{}, err
	} else if revoked {
		return oid4vci.TokenResponse{}, refuse(oid4vci.ErrInvalidGrant, "the offer has been revoked")
	}

	token, err := randomToken()
	if err != nil {
		return oid4vci.TokenResponse{}, err
	}
	if err := s.sessions.SaveToken(ctx, AccessToken{
		Hash: hash(token), SessionID: session.ID, JKT: proof.JKT, ClientID: att.ClientID,
		ExpiresAt: now.Add(s.cfg.TokenTTL),
	}); err != nil {
		return oid4vci.TokenResponse{}, err
	}
	return oid4vci.TokenResponse{
		AccessToken:          token,
		TokenType:            "DPoP",
		ExpiresIn:            int64(s.cfg.TokenTTL.Seconds()),
		AuthorizationDetails: authorizationDetails(session),
	}, nil
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
