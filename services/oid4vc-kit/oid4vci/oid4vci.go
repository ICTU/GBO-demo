// Package oid4vci holds the messages of OpenID for Verifiable Credential
// Issuance 1.0 that both sides of the protocol need, and the verification
// of key proofs.
package oid4vci

// GrantPreAuthorizedCode is the grant type of the Pre-Authorized Code Flow
// (section 4.1.1).
const GrantPreAuthorizedCode = "urn:ietf:params:oauth:grant-type:pre-authorized_code"

// AuthorizationDetailsType is the authorization_details type of OpenID4VCI
// (section 5.1.1).
const AuthorizationDetailsType = "openid_credential"

// Error codes of the token endpoint (RFC 6749, RFC 9449,
// draft-ietf-oauth-attestation-based-client-auth) and the credential
// endpoint (section 8.3.1.2).
const (
	ErrInvalidRequest           = "invalid_request"
	ErrInvalidGrant             = "invalid_grant"
	ErrUnsupportedGrantType     = "unsupported_grant_type"
	ErrInvalidDPoPProof         = "invalid_dpop_proof"
	ErrInvalidClientAttestation = "invalid_client_attestation"
	ErrUseFreshAttestation      = "use_fresh_attestation"
	ErrUseAttestationChallenge  = "use_attestation_challenge"
	ErrInvalidToken             = "invalid_token"
	ErrInvalidCredentialRequest = "invalid_credential_request"
	ErrUnknownCredentialID      = "unknown_credential_identifier"
	ErrInvalidProof             = "invalid_proof"
	ErrInvalidNonce             = "invalid_nonce"
	ErrCredentialRequestDenied  = "credential_request_denied"
	// ErrInvalidEncryptionParameters: encryption parameters that are
	// invalid, or missing where encryption is required.
	ErrInvalidEncryptionParameters = "invalid_encryption_parameters"
	ErrServerError                 = "server_error"
)

// CredentialOffer is sent to the wallet by value or by reference
// (section 4.1.1).
type CredentialOffer struct {
	CredentialIssuer           string         `json:"credential_issuer"`
	CredentialConfigurationIDs []string       `json:"credential_configuration_ids"`
	Grants                     map[string]any `json:"grants,omitempty"`
}

// PreAuthorizedCodeGrant is the grant object for GrantPreAuthorizedCode.
type PreAuthorizedCodeGrant struct {
	PreAuthorizedCode string  `json:"pre-authorized_code"`
	TxCode            *TxCode `json:"tx_code,omitempty"`
}

// TxCode tells the wallet that a transaction code is needed.
type TxCode struct {
	InputMode   string `json:"input_mode,omitempty"`
	Length      int    `json:"length,omitempty"`
	Description string `json:"description,omitempty"`
}

// AuthorizationDetail is one authorization_details entry in the token
// response (section 6.2).
type AuthorizationDetail struct {
	Type                      string   `json:"type"`
	CredentialConfigurationID string   `json:"credential_configuration_id"`
	CredentialIdentifiers     []string `json:"credential_identifiers"`
}

// TokenResponse is the successful token response (section 6.2).
type TokenResponse struct {
	AccessToken          string                `json:"access_token"`
	TokenType            string                `json:"token_type"`
	ExpiresIn            int64                 `json:"expires_in"`
	AuthorizationDetails []AuthorizationDetail `json:"authorization_details,omitempty"`
}

// NonceResponse is the nonce endpoint response (section 7.2).
type NonceResponse struct {
	CNonce string `json:"c_nonce"`
}

// Proofs carries the key proofs of a credential request (section 8.2).
type Proofs struct {
	JWT         []string `json:"jwt,omitempty"`
	Attestation []string `json:"attestation,omitempty"`
}

// CredentialRequest is a credential request (section 8.2).
type CredentialRequest struct {
	CredentialIdentifier      string  `json:"credential_identifier,omitempty"`
	CredentialConfigurationID string  `json:"credential_configuration_id,omitempty"`
	Proofs                    *Proofs `json:"proofs,omitempty"`
	// ResponseEncryption asks for an encrypted response (section 8.2).
	ResponseEncryption *ResponseEncryption `json:"credential_response_encryption,omitempty"`
}

// IssuedCredential is one entry of a credential response.
type IssuedCredential struct {
	Credential string `json:"credential"`
}

// CredentialResponse is an immediate credential response (section 8.3).
type CredentialResponse struct {
	Credentials []IssuedCredential `json:"credentials"`
}

// ErrorResponse is the error body of the token and credential endpoints.
type ErrorResponse struct {
	Error       string `json:"error"`
	Description string `json:"error_description,omitempty"`
}
