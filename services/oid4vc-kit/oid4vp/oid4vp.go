// Package oid4vp holds the verifier side of OpenID for Verifiable
// Presentations 1.0 as HAIP 1.0 profiles it: the signed request object a
// wallet fetches by reference, and the verification of the encrypted
// response (direct_post.jwt) with one SD-JWT VC and its key binding. HTTP
// routes and sessions belong to the caller.
package oid4vp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"regexp"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/oid4vc-kit/sdjwtvc"
)

// Values of the request object (sections 5 and 8.3).
const (
	RequestObjectType  = "oauth-authz-req+jwt"
	SelfIssuedAudience = "https://self-issued.me/v2"
	ResponseTypeVP     = "vp_token"
	ResponseModeJWT    = "direct_post.jwt"
	ClientIDPrefixHash = "x509_hash:"
	// FormatRegistrationCert is the verifier_info format of a WRPRC (ETSI
	// TS 119 475).
	FormatRegistrationCert = "registration_cert"
	KeyBindingType         = "kb+jwt"
)

// EncryptionAlgorithm and EncryptionMethods are the response encryption the
// verifier asks for: ECDH-ES on P-256 with A128GCM or A256GCM (HAIP 1.0).
const EncryptionAlgorithm = jose.ECDH_ES

var EncryptionMethods = []jose.ContentEncryption{jose.A128GCM, jose.A256GCM}

// CredentialSignerEKU is the extended key usage the NL Wallet requires of
// an attestation signer (the mDL document signer of ISO/IEC 18013-5); the
// verifier requires it too.
var CredentialSignerEKU = asn1.ObjectIdentifier{1, 0, 18013, 5, 1, 2}

// ClockSkew is allowed on the key binding's iat and on the credential's
// nbf and exp; KeyBindingMaxAge bounds how old a key binding may be.
const (
	ClockSkew        = time.Minute
	KeyBindingMaxAge = 5 * time.Minute
)

// RegistrationEncoding is how verifier_info carries the WRPRC. The wallets
// differ: the NL Wallet base64url-decodes the data, the EUDI wallets read
// the compact JWT as is.
type RegistrationEncoding int

const (
	RegistrationBase64URL RegistrationEncoding = iota
	RegistrationJWT
)

// Query is a DCQL query (section 6) with exactly one credential query.
type Query struct {
	Credentials []CredentialQuery `json:"credentials"`
}

// CredentialQuery asks for one SD-JWT VC of one of VCTValues with the
// claims at the given paths.
type CredentialQuery struct {
	ID     string       `json:"id"`
	Format string       `json:"format"`
	Meta   Meta         `json:"meta"`
	Claims []ClaimQuery `json:"claims"`
}

// Meta holds the type constraint of an SD-JWT VC query.
type Meta struct {
	VCTValues []string `json:"vct_values"`
}

// ClaimQuery names a claim by its path of property names.
type ClaimQuery struct {
	Path []string `json:"path"`
}

var queryID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validate requires what the request and the response checks rely on.
func (s Session) validate() error {
	if s.ClientID == "" || s.Nonce == "" || s.State == "" || s.Key.Key == nil || s.Key.KeyID == "" {
		return errors.New("oid4vp: the session needs a client_id, nonce, state and response key")
	}
	return s.Query.validate()
}

func (q Query) validate() error {
	if len(q.Credentials) != 1 {
		return errors.New("oid4vp: the query must hold exactly one credential query")
	}
	c := q.Credentials[0]
	if !queryID.MatchString(c.ID) || c.Format != sdjwtvc.Type || len(c.Meta.VCTValues) == 0 || len(c.Claims) == 0 {
		return errors.New("oid4vp: the credential query needs an id, format dc+sd-jwt, vct_values and claims")
	}
	for _, cl := range c.Claims {
		if len(cl.Path) == 0 {
			return errors.New("oid4vp: a claim query needs a path")
		}
	}
	return nil
}

// ResponseKey is the ephemeral key a wallet encrypts its response to,
// one per session.
type ResponseKey struct {
	KeyID string
	Key   *ecdsa.PrivateKey
}

// NewResponseKey returns a fresh P-256 key with a random kid.
func NewResponseKey() (ResponseKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return ResponseKey{}, err
	}
	kid := make([]byte, 16)
	if _, err := rand.Read(kid); err != nil {
		return ResponseKey{}, err
	}
	return ResponseKey{KeyID: base64.RawURLEncoding.EncodeToString(kid), Key: key}, nil
}

// PublicJWK is the published form of the key, with kid, alg and use as the
// wallets require.
func (k ResponseKey) PublicJWK() jose.JSONWebKey {
	return jose.JSONWebKey{Key: k.Key.Public(), KeyID: k.KeyID, Algorithm: string(EncryptionAlgorithm), Use: "enc"}
}

// Session is what the verifier keeps between handing out the request and
// receiving the response.
type Session struct {
	ClientID string
	Nonce    string
	State    string
	Query    Query
	Key      ResponseKey
}
