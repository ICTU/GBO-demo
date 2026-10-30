package issuance

import (
	"encoding/base64"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/clientattest"
	"gbo-demo/oid4vc-kit/dpop"
	"gbo-demo/oid4vc-kit/oid4vci"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/x5c"
)

// metadataType is the typ of signed issuer metadata (section 12.2.3).
const metadataType = "openidvci-issuer-metadata+jwt"

// metadataLifetime is how long signed metadata stays valid.
const metadataLifetime = time.Hour

// IssuerMetadataJSON returns the unsigned Credential Issuer Metadata of a
// profile, which section 12.2.2 requires next to the signed form.
func (s *Service) IssuerMetadataJSON(profile string) (map[string]any, error) {
	p, ok := s.profiles[profile]
	if !ok {
		return nil, ErrUnknownProfile
	}
	return s.issuerMetadata(p), nil
}

// IssuerMetadata returns the Credential Issuer Metadata of a profile as a
// JWT signed with its access certificate (section 12.2.3; ARF ISSU_32).
func (s *Service) IssuerMetadata(profile string) (string, error) {
	p, ok := s.profiles[profile]
	if !ok {
		return "", ErrUnknownProfile
	}
	md := s.issuerMetadata(p)
	now := s.now()
	md["sub"] = s.Identifier(p.Name)
	md["iat"] = now.Unix()
	md["exp"] = now.Add(metadataLifetime).Unix()
	opts := (&jose.SignerOptions{}).WithType(metadataType).WithHeader("x5c", x5c.Header(p.AccessChain))
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: p.AccessKey}, opts)
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(md).Serialize()
}

func (s *Service) issuerMetadata(p Profile) map[string]any {
	id := s.Identifier(p.Name)
	md := map[string]any{
		"credential_issuer":   id,
		"credential_endpoint": id + "/credential",
		"nonce_endpoint":      id + "/nonce",
	}
	if s.cfg.BatchSize >= 2 {
		md["batch_credential_issuance"] = map[string]any{"batch_size": s.cfg.BatchSize}
	}
	if p.Kind == KindNL {
		// Not part of OpenID4VCI: the NL Wallet requires a preview of the
		// credentials before it asks for them.
		md["credential_preview_endpoint"] = id + "/credential_preview"
	}
	if len(p.RegistrationCertificate) > 0 {
		md["issuer_info"] = []map[string]string{{
			"format": "registration_cert",
			"data":   base64.RawURLEncoding.EncodeToString(p.RegistrationCertificate),
		}}
	}
	configs := map[string]any{}
	for _, c := range s.cfg.Configurations {
		configs[c.ID] = s.configurationMetadata(p, c)
	}
	md["credential_configurations_supported"] = configs
	return md
}

func (s *Service) configurationMetadata(p Profile, c CredentialConfiguration) map[string]any {
	proofType := map[string]any{"proof_signing_alg_values_supported": algNames(oid4vci.ProofAlgorithms)}
	proofs := map[string]any{"jwt": proofType}
	if p.Kind == KindEUDI {
		required := map[string]any{"key_storage": p.KeyStorage, "user_authentication": p.UserAuthentication}
		proofType["key_attestations_required"] = required
		proofs["attestation"] = map[string]any{
			"proof_signing_alg_values_supported": algNames(oid4vci.ProofAlgorithms),
			"key_attestations_required":          required,
		}
	}
	md := map[string]any{
		"format": sdjwtvc.Type,
		"vct":    c.VCT,
		"cryptographic_binding_methods_supported": []string{"jwk"},
		"credential_signing_alg_values_supported": []string{string(jose.ES256)},
		"proof_types_supported":                   proofs,
		"credential_metadata":                     map[string]any{"display": c.Display, "claims": c.Claims},
		"scope":                                   c.Scope,
	}
	return md
}

// AuthorizationServerMetadata returns the OAuth 2.0 Authorization Server
// Metadata (RFC 8414) of a profile. Phase 1 offers only the Pre-Authorized
// Code Flow; the Authorization Code Flow follows in phase 3.
func (s *Service) AuthorizationServerMetadata(profile string) (map[string]any, error) {
	p, ok := s.profiles[profile]
	if !ok {
		return nil, ErrUnknownProfile
	}
	id := s.Identifier(p.Name)
	return map[string]any{
		"issuer":                                              id,
		"token_endpoint":                                      id + "/token",
		"response_types_supported":                            []string{},
		"grant_types_supported":                               []string{oid4vci.GrantPreAuthorizedCode},
		"token_endpoint_auth_methods_supported":               []string{clientattest.Method},
		"client_attestation_signing_alg_values_supported":     algNames(clientattest.Algorithms),
		"client_attestation_pop_signing_alg_values_supported": algNames(clientattest.Algorithms),
		"challenge_endpoint":                                  id + "/challenge",
		"dpop_signing_alg_values_supported":                   algNames(dpop.Algorithms),
		"pre-authorized_grant_anonymous_access_supported":     false,
	}, nil
}

func algNames(algs []jose.SignatureAlgorithm) []string {
	out := make([]string, len(algs))
	for i, a := range algs {
		out[i] = string(a)
	}
	return out
}
