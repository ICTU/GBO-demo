package oid4vp

import (
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/x5c"
)

// RequestSigner signs request objects with the key of a verifier's access
// certificate (WRPAC).
type RequestSigner struct {
	signer   jose.Signer
	clientID string
}

// NewRequestSigner returns a signer for key with chain, leaf first and
// without the root, in x5c. The client_id is the x509_hash of the leaf
// (section 5.9.3).
func NewRequestSigner(key crypto.Signer, chain []*x509.Certificate) (*RequestSigner, error) {
	if len(chain) == 0 {
		return nil, errors.New("oid4vp: the certificate chain is empty")
	}
	opts := (&jose.SignerOptions{}).WithType(RequestObjectType).WithHeader("x5c", x5c.Header(chain))
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(chain[0].Raw)
	return &RequestSigner{signer: s, clientID: ClientIDPrefixHash + base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// ClientID is the client_id of the signer, with its prefix.
func (s *RequestSigner) ClientID() string { return s.clientID }

// Request describes one request object.
type Request struct {
	Session     Session
	ResponseURI string
	// RegistrationCertificate is the verifier's WRPRC as a compact JWT,
	// carried in verifier_info as Encoding says.
	RegistrationCertificate string
	Encoding                RegistrationEncoding
	IssuedAt                time.Time
	Expires                 time.Time
}

// Sign returns the signed request object, for the wallet to fetch from
// request_uri. The session's ClientID must be the signer's.
func (s *RequestSigner) Sign(r Request) (string, error) {
	ses := r.Session
	if ses.ClientID != s.clientID {
		return "", errors.New("oid4vp: the session is not for this client_id")
	}
	if err := ses.validate(); err != nil {
		return "", err
	}
	if r.ResponseURI == "" {
		return "", errors.New("oid4vp: response_uri is required")
	}
	if r.RegistrationCertificate == "" || !r.Expires.After(r.IssuedAt) {
		return "", errors.New("oid4vp: a registration certificate and a validity period are required")
	}
	data := r.RegistrationCertificate
	if r.Encoding == RegistrationBase64URL {
		data = base64.RawURLEncoding.EncodeToString([]byte(data))
	}
	claims := map[string]any{
		"client_id":     s.clientID,
		"aud":           SelfIssuedAudience,
		"iat":           r.IssuedAt.Unix(),
		"exp":           r.Expires.Unix(),
		"response_type": ResponseTypeVP,
		"response_mode": ResponseModeJWT,
		"response_uri":  r.ResponseURI,
		"nonce":         ses.Nonce,
		"state":         ses.State,
		"dcql_query":    ses.Query,
		"client_metadata": map[string]any{
			"jwks": map[string]any{"keys": []jose.JSONWebKey{ses.Key.PublicJWK()}},
			"encrypted_response_enc_values_supported": EncryptionMethods,
			"vp_formats_supported": map[string]any{
				sdjwtvc.Type: map[string]any{"sd-jwt_alg_values": []string{"ES256"}, "kb-jwt_alg_values": []string{"ES256"}},
			},
		},
		"verifier_info": []map[string]any{{"format": FormatRegistrationCert, "data": data}},
	}
	return jwt.Signed(s.signer).Claims(claims).Serialize()
}
