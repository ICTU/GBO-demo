package issuance

import (
	"crypto/x509"
	"encoding/base64"
	"strings"
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
// target, which section 12.2.2 requires next to the signed form.
func (s *Service) IssuerMetadataJSON(t Target) (map[string]any, error) {
	prov, p, err := s.resolve(t)
	if err != nil {
		return nil, err
	}
	return s.issuerMetadata(t, prov, p), nil
}

// IssuerMetadata returns the Credential Issuer Metadata of a target as a
// JWT signed with its provider's access certificate (section 12.2.3; ARF
// ISSU_32).
func (s *Service) IssuerMetadata(t Target) (string, error) {
	prov, p, err := s.resolve(t)
	if err != nil {
		return "", err
	}
	md := s.issuerMetadata(t, prov, p)
	now := s.now()
	md["sub"] = s.Identifier(t)
	md["iat"] = now.Unix()
	md["exp"] = now.Add(metadataLifetime).Unix()
	opts := (&jose.SignerOptions{}).WithType(metadataType).WithHeader("x5c", x5c.Header(prov.AccessChain))
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: prov.AccessKey}, opts)
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(md).Serialize()
}

// issuerMetadata lists only the provider's own credential configurations.
func (s *Service) issuerMetadata(t Target, prov *provider, p Profile) map[string]any {
	id := s.Identifier(t)
	md := map[string]any{
		"credential_issuer":   id,
		"credential_endpoint": id + "/credential",
		"nonce_endpoint":      id + "/nonce",
	}
	// Wallets show the issuer by this name (section 12.2.4). An
	// organization name needs no translation, so it has no locale.
	md["display"] = []map[string]string{{"name": issuerName(prov.AccessChain[0])}}
	if s.cfg.BatchSize >= 2 {
		md["batch_credential_issuance"] = map[string]any{"batch_size": s.cfg.BatchSize}
	}
	if p.Kind == KindEUDI {
		md["notification_endpoint"] = id + "/notification"
	}
	if s.offersLogin(p) {
		md["deferred_credential_endpoint"] = id + "/deferred_credential"
	}
	if p.servesNL() {
		// Not part of OpenID4VCI: the NL Wallet requires a preview of the
		// credentials before it asks for them.
		md["credential_preview_endpoint"] = id + "/credential_preview"
	}
	if p.servesEUDI() {
		// Supported, not required (section 12.2.4). A wallet that asks for
		// an encrypted response must encrypt its request (section 8.2).
		methods := encNames(oid4vci.EncryptionMethods)
		md["credential_request_encryption"] = map[string]any{
			"jwks":                 map[string]any{"keys": []jose.JSONWebKey{prov.requestKey.PublicJWK()}},
			"enc_values_supported": methods,
			"encryption_required":  false,
		}
		md["credential_response_encryption"] = map[string]any{
			"alg_values_supported": []string{string(oid4vci.EncryptionAlgorithm)},
			"enc_values_supported": methods,
			"encryption_required":  false,
		}
	}
	if len(prov.RegistrationCertificate) > 0 {
		md["issuer_info"] = []map[string]string{{
			"format": "registration_cert",
			"data":   base64.RawURLEncoding.EncodeToString(prov.RegistrationCertificate),
		}}
	}
	configs := map[string]any{}
	for _, c := range s.cfg.Configurations {
		if c.Provider == prov.Name {
			configs[c.ID] = s.configurationMetadata(p, c)
		}
	}
	md["credential_configurations_supported"] = configs
	return md
}

func (s *Service) configurationMetadata(p Profile, c CredentialConfiguration) map[string]any {
	proofType := map[string]any{"proof_signing_alg_values_supported": algNames(oid4vci.ProofAlgorithms)}
	proofs := map[string]any{"jwt": proofType}
	if p.servesEUDI() {
		required := map[string]any{"key_storage": p.KeyStorage, "user_authentication": p.UserAuthentication}
		// On KindCombined the jwt proof type is the NL Wallet's, without key
		// attestation; whether a wallet must attest follows from its WIA.
		if p.Kind == KindEUDI {
			proofType["key_attestations_required"] = required
		}
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

// issuerName is the organization name of the provider's access
// certificate.
func issuerName(access *x509.Certificate) string {
	if len(access.Subject.Organization) == 0 {
		return ""
	}
	return strings.TrimSpace(access.Subject.Organization[0])
}

// AuthorizationServerMetadata returns the OAuth 2.0 Authorization Server
// Metadata (RFC 8414) of a target: the Pre-Authorized Code Flow, and on a
// profile that offers it the Authorization Code Flow with required PAR
// (RFC 9126) and iss in the authorization response (RFC 9207).
func (s *Service) AuthorizationServerMetadata(t Target) (map[string]any, error) {
	_, p, err := s.resolve(t)
	if err != nil {
		return nil, err
	}
	id := s.Identifier(t)
	md := map[string]any{
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
	}
	if p.Kind == KindEUDI {
		md["grant_types_supported"] = []string{oid4vci.GrantPreAuthorizedCode, oid4vci.GrantRefreshToken}
	}
	if s.offersLogin(p) {
		md["authorization_endpoint"] = id + "/authorize"
		md["pushed_authorization_request_endpoint"] = id + "/par"
		md["require_pushed_authorization_requests"] = true
		md["response_types_supported"] = []string{"code"}
		md["grant_types_supported"] = []string{oid4vci.GrantPreAuthorizedCode, GrantAuthorizationCode, oid4vci.GrantRefreshToken}
		md["code_challenge_methods_supported"] = []string{"S256"}
		md["authorization_response_iss_parameter_supported"] = true
	}
	return md, nil
}

func encNames(encs []jose.ContentEncryption) []string {
	out := make([]string, len(encs))
	for i, e := range encs {
		out[i] = string(e)
	}
	return out
}

func algNames(algs []jose.SignatureAlgorithm) []string {
	out := make([]string, len(algs))
	for i, a := range algs {
		out[i] = string(a)
	}
	return out
}
