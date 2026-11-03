package issuance

import (
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/dpop"
	"gbo-demo/oid4vc-kit/keyattest"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
)

// Nonce returns a fresh c_nonce (section 7); it is accepted once.
func (s *Service) Nonce(ctx context.Context, t Target) (string, error) {
	if _, _, err := s.resolve(t); err != nil {
		return "", err
	}
	n, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := s.onetime.Put(ctx, scoped(kindNonce, s.Identifier(t)), n, s.now().Add(s.cfg.NonceTTL)); err != nil {
		return "", err
	}
	return n, nil
}

// CredentialCall is a credential request with its authorization.
type CredentialCall struct {
	Target Target
	// Authorization is the Authorization header: "DPoP <access token>".
	Authorization string
	DPoP          []string
	Request       oid4vci.CredentialRequest
	// Encrypted tells that the wallet sent the request as a JWE
	// (application/jwt, section 10), held in EncryptedRequest; Request is
	// then ignored.
	Encrypted        bool
	EncryptedRequest string
}

// CredentialResult is a credential response: Encrypted holds it as a JWE
// when the wallet asked for an encrypted response, Response otherwise.
type CredentialResult struct {
	Response  oid4vci.CredentialResponse
	Encrypted string
}

// Credential issues the credentials of one credential identifier, one per
// holder key, in the order of the keys (section 8). The keys come from the
// proofs; for KindEUDI from the key attestation they carry, as HAIP and ARF
// ISSU_30 require.
func (s *Service) Credential(ctx context.Context, call CredentialCall) (CredentialResult, error) {
	prov, p, err := s.resolve(call.Target)
	if err != nil {
		return CredentialResult{}, err
	}
	now := s.now()
	id := s.Identifier(call.Target)

	token, session, err := s.authorize(ctx, call.Target, call.Authorization, "DPoP")
	if err != nil {
		return CredentialResult{}, err
	}
	accessToken := strings.TrimSpace(call.Authorization[len("DPoP"):])
	proof, err := dpop.Verify(call.DPoP, dpop.Expected{Method: "POST", URL: id + "/credential", AccessToken: accessToken}, now)
	if err != nil {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidDPoPProof, err.Error())
	}
	if proof.JKT != token.JKT {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidDPoPProof, "the DPoP key is not the key the access token is bound to")
	}
	if ok, err := s.onetime.FirstUse(ctx, kindDPoP, proof.JTI, proof.IAT.Add(dpop.Window), now); err != nil {
		return CredentialResult{}, err
	} else if !ok {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidDPoPProof, "the DPoP proof has been used before")
	}

	req, encrypter, err := s.openRequest(prov, p, call)
	if err != nil {
		return CredentialResult{}, err
	}
	if req.CredentialConfigurationID != "" || req.CredentialIdentifier == "" {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidCredentialRequest, "ask for a credential_identifier from the token response")
	}
	doc, ok := findDocument(session, req.CredentialIdentifier)
	if !ok {
		return CredentialResult{}, refuse(oid4vci.ErrUnknownCredentialID, "unknown credential_identifier")
	}
	if revoked, err := s.referenceRevoked(ctx, session); err != nil {
		return CredentialResult{}, err
	} else if revoked {
		return CredentialResult{}, refuse(oid4vci.ErrCredentialRequestDenied, "the offer has been revoked")
	}
	keys, err := s.holderKeys(ctx, p, id, req.Proofs)
	if err != nil {
		return CredentialResult{}, err
	}
	// After a configuration change the configuration may belong to another
	// provider; this provider then no longer offers it.
	c, ok := s.configs[doc.ConfigurationID]
	if !ok || c.Provider != prov.Name {
		return CredentialResult{}, refuse(oid4vci.ErrUnknownCredentialID, "the credential configuration is no longer offered")
	}

	// The credential is reserved before anything is allocated for it, so a
	// repeated or concurrent request uses up no status list entries. The
	// reservation is released when issuing fails, and expires if the
	// process dies in between.
	owner, err := randomToken()
	if err != nil {
		return CredentialResult{}, err
	}
	if reserved, err := s.sessions.ReserveIssue(ctx, session.ID, doc.CredentialID, owner, now, now.Add(issueReservation)); err != nil {
		return CredentialResult{}, err
	} else if !reserved {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidCredentialRequest, "this credential has been issued already or is being issued")
	}
	resp, err := s.issue(ctx, prov, c, doc, session.Reference, keys, now)
	out := CredentialResult{Response: resp}
	// The wallet's key was checked before issuing, so encrypting fails only
	// on an internal error; like a failed issue, that leaves the allocated
	// status entries unused.
	if err == nil && encrypter != nil {
		out, err = encryptResult(encrypter, resp)
	}
	if err != nil {
		if rerr := s.sessions.ReleaseIssue(ctx, session.ID, doc.CredentialID, owner); rerr != nil {
			return CredentialResult{}, errors.Join(err, rerr)
		}
		return CredentialResult{}, err
	}
	// A request that took longer than its reservation may have lost it to
	// another; then the other issues, and this one returns nothing.
	if completed, err := s.sessions.CompleteIssue(ctx, session.ID, doc.CredentialID, owner); err != nil {
		return CredentialResult{}, err
	} else if !completed {
		return CredentialResult{}, refuse(oid4vci.ErrInvalidCredentialRequest, "the reservation for this credential was lost to another request")
	}
	return out, nil
}

// openRequest returns the credential request of a call, decrypted when the
// wallet encrypted it, and the encrypter for the response when the wallet
// asked for an encrypted one. Only KindEUDI publishes encryption.
func (s *Service) openRequest(prov *provider, p Profile, call CredentialCall) (oid4vci.CredentialRequest, jose.Encrypter, error) {
	req := call.Request
	encrypted := call.Encrypted
	if p.Kind != KindEUDI && (encrypted || req.ResponseEncryption != nil) {
		return req, nil, refuse(oid4vci.ErrInvalidCredentialRequest, "encryption is not supported for this profile")
	}
	if encrypted {
		body, err := oid4vci.DecryptRequest(call.EncryptedRequest, []oid4vci.RequestKey{prov.requestKey})
		if err != nil {
			return req, nil, refuse(oid4vci.ErrInvalidCredentialRequest, err.Error())
		}
		req = oid4vci.CredentialRequest{}
		if err := json.Unmarshal(body, &req); err != nil {
			return req, nil, refuse(oid4vci.ErrInvalidCredentialRequest, "the decrypted request is not a JSON credential request")
		}
	}
	if req.ResponseEncryption == nil {
		return req, nil, nil
	}
	// Section 8.2: otherwise an attacker could substitute the key.
	if !encrypted {
		return req, nil, refuse(oid4vci.ErrInvalidEncryptionParameters, "an encrypted response must be asked for in an encrypted request")
	}
	e, err := oid4vci.NewResponseEncrypter(*req.ResponseEncryption)
	if err != nil {
		return req, nil, refuse(oid4vci.ErrInvalidEncryptionParameters, err.Error())
	}
	return req, e, nil
}

// encryptResult encrypts the credential response (section 10); errors are
// not encrypted.
func encryptResult(e jose.Encrypter, resp oid4vci.CredentialResponse) (CredentialResult, error) {
	body, err := json.Marshal(resp)
	if err != nil {
		return CredentialResult{}, err
	}
	jwe, err := oid4vci.EncryptResponse(e, body)
	if err != nil {
		return CredentialResult{}, err
	}
	return CredentialResult{Encrypted: jwe}, nil
}

// issueReservation is how long a credential stays reserved for the request
// that issues it.
const issueReservation = time.Minute

// issue signs one credential per holder key, each with its own status list
// entry.
func (s *Service) issue(ctx context.Context, prov *provider, c CredentialConfiguration, doc Document, reference string, keys []jose.JSONWebKey, now time.Time) (oid4vci.CredentialResponse, error) {
	// iat is rounded to the day, so the copies of a batch cannot be linked
	// by it (RFC 9901 section 10.1).
	issuedAt := now.UTC().Truncate(24 * time.Hour)
	out := oid4vci.CredentialResponse{}
	for _, k := range keys {
		idx, err := s.status.Allocate(ctx, statusKey(prov.Name, c.StatusList), s.cfg.StatusListSize, reference)
		if err != nil {
			return oid4vci.CredentialResponse{}, err
		}
		cred, err := prov.signer.Issue(sdjwtvc.Credential{
			VCT:       c.VCT,
			IssuedAt:  issuedAt,
			NotBefore: doc.NotBefore,
			Expires:   doc.Expires,
			HolderKey: &k,
			Status:    &sdjwtvc.StatusReference{Index: idx, URI: s.StatusListURI(prov.Name, c.StatusList)},
			Claims:    doc.Claims,
		})
		if err != nil {
			return oid4vci.CredentialResponse{}, err
		}
		out.Credentials = append(out.Credentials, oid4vci.IssuedCredential{Credential: cred})
	}
	return out, nil
}

// referenceRevoked reports whether the session's offer reference has been
// revoked: then nothing more is issued for it.
func (s *Service) referenceRevoked(ctx context.Context, session Session) (bool, error) {
	if session.Reference == "" {
		return false, nil
	}
	return s.status.ReferenceRevoked(ctx, session.Reference)
}

// authorize resolves the access token of an Authorization header with the
// given scheme to an unexpired token and its session of this profile.
func (s *Service) authorize(ctx context.Context, t Target, header, scheme string) (AccessToken, Session, error) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], scheme) || strings.TrimSpace(parts[1]) == "" {
		return AccessToken{}, Session{}, refuse(oid4vci.ErrInvalidToken, "a "+scheme+" access token is required")
	}
	token, session, err := s.sessions.Token(ctx, hash(strings.TrimSpace(parts[1])), s.now())
	if errors.Is(err, ErrNotFound) || (err == nil && (session.Provider != t.Provider || session.Profile != t.Profile)) {
		return AccessToken{}, Session{}, refuse(oid4vci.ErrInvalidToken, "the access token is unknown or expired")
	}
	return token, session, err
}

// holderKeys verifies the key proofs and returns the keys to bind the
// credentials to. All proofs must carry the same c_nonce, which is used up.
func (s *Service) holderKeys(ctx context.Context, p Profile, id string, proofs *oid4vci.Proofs) ([]jose.JSONWebKey, error) {
	if proofs == nil || (len(proofs.JWT) == 0) == (len(proofs.Attestation) == 0) {
		return nil, refuse(oid4vci.ErrInvalidProof, "proofs must contain jwt or attestation proofs")
	}
	now := s.now()
	var keys []jose.JSONWebKey
	var nonces []string

	switch {
	case len(proofs.Attestation) > 0:
		if p.Kind != KindEUDI {
			return nil, refuse(oid4vci.ErrInvalidProof, "the attestation proof type is not supported for this profile")
		}
		if len(proofs.Attestation) != 1 {
			return nil, refuse(oid4vci.ErrInvalidProof, "exactly one attestation proof is expected")
		}
		ka, err := keyattest.Verify(proofs.Attestation[0], p.KeyAttestationProviders, now, false)
		if err != nil {
			return nil, refuse(oid4vci.ErrInvalidProof, err.Error())
		}
		if err := checkLevels(p, ka); err != nil {
			return nil, err
		}
		keys = ka.Keys
		nonces = append(nonces, ka.Nonce)
	default:
		for _, raw := range proofs.JWT {
			jp, err := oid4vci.VerifyJWTProof(raw, id, now)
			if err != nil {
				return nil, refuse(oid4vci.ErrInvalidProof, err.Error())
			}
			nonces = append(nonces, jp.Nonce)
			if p.Kind != KindEUDI {
				// One credential per proof: two proofs with one key would
				// silently yield fewer credentials than proofs.
				if containsKey(keys, jp.Key) {
					return nil, refuse(oid4vci.ErrInvalidProof, "two proofs use the same key")
				}
				keys = append(keys, jp.Key)
				continue
			}
			// HAIP and the ARF: the proof key must be attested, and every
			// attested key gets a credential.
			if jp.KeyAttestation == "" {
				return nil, refuse(oid4vci.ErrInvalidProof, "a key attestation is required")
			}
			ka, err := keyattest.Verify(jp.KeyAttestation, p.KeyAttestationProviders, now, true)
			if err != nil {
				return nil, refuse(oid4vci.ErrInvalidProof, err.Error())
			}
			if err := checkLevels(p, ka); err != nil {
				return nil, err
			}
			if !containsKey(ka.Keys, jp.Key) {
				return nil, refuse(oid4vci.ErrInvalidProof, "the proof is not signed by an attested key")
			}
			if ka.Nonce != jp.Nonce {
				return nil, refuse(oid4vci.ErrInvalidProof, "the key attestation carries another nonce than the proof")
			}
			keys = append(keys, ka.Keys...)
		}
	}

	keys, err := distinct(keys)
	if err != nil {
		return nil, err
	}
	if len(keys) > s.cfg.BatchSize {
		return nil, refuse(oid4vci.ErrInvalidProof, fmt.Sprintf("at most %d keys per request", s.cfg.BatchSize))
	}
	for _, n := range nonces {
		// A proof without c_nonce is an invalid proof (section 8.3.1.2).
		if n == "" || n != nonces[0] {
			return nil, refuse(oid4vci.ErrInvalidProof, "every proof must carry the same c_nonce")
		}
	}
	if ok, err := s.onetime.Take(ctx, scoped(kindNonce, id), nonces[0], now); err != nil {
		return nil, err
	} else if !ok {
		return nil, refuse(oid4vci.ErrInvalidNonce, "the c_nonce is unknown, expired or used")
	}
	return keys, nil
}

// distinct removes repeated keys: a key attested in more than one key
// attestation of a request gets one credential.
func distinct(keys []jose.JSONWebKey) ([]jose.JSONWebKey, error) {
	seen := map[string]bool{}
	var out []jose.JSONWebKey
	for _, k := range keys {
		tp, err := thumbprint(k)
		if err != nil {
			return nil, refuse(oid4vci.ErrInvalidProof, "a proof key has no thumbprint")
		}
		if seen[tp] {
			continue
		}
		seen[tp] = true
		out = append(out, k)
	}
	return out, nil
}

// checkLevels requires the key attestation to assert at least one of the
// key storage and one of the user authentication levels of the profile
// (ARF ISSU_27d).
func checkLevels(p Profile, ka keyattest.Verified) error {
	if !anyOf(ka.KeyStorage, p.KeyStorage) || !anyOf(ka.UserAuthentication, p.UserAuthentication) {
		return refuse(oid4vci.ErrInvalidProof, "the key attestation does not assert the required key storage and user authentication levels")
	}
	return nil
}

func anyOf(have, want []string) bool {
	for _, h := range have {
		if slices.Contains(want, h) {
			return true
		}
	}
	return false
}

func containsKey(keys []jose.JSONWebKey, k jose.JSONWebKey) bool {
	want, err := thumbprint(k)
	if err != nil {
		return false
	}
	for _, c := range keys {
		if tp, err := thumbprint(c); err == nil && tp == want {
			return true
		}
	}
	return false
}

func thumbprint(k jose.JSONWebKey) (string, error) {
	tp, err := k.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(tp), nil
}

func findDocument(s Session, credentialID string) (Document, bool) {
	for _, d := range s.Documents {
		if d.CredentialID == credentialID {
			return d, true
		}
	}
	return Document{}, false
}
