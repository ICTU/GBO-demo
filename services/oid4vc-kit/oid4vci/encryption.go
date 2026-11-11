package oid4vci

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/json"
	"errors"

	"github.com/go-jose/go-jose/v4"
)

// EncryptionAlgorithm is the only key agreement for encrypted credential
// requests and responses: ECDH-ES on P-256, as HAIP requires for
// presentation.
const EncryptionAlgorithm = jose.ECDH_ES

// EncryptionMethods are the accepted JWE enc values, published as
// enc_values_supported.
var EncryptionMethods = []jose.ContentEncryption{jose.A128GCM, jose.A256GCM}

// ResponseEncryption is the credential_response_encryption object of a
// credential request (section 8.2). JWK is kept raw, so a malformed key is
// an invalid encryption parameter rather than an unreadable request.
type ResponseEncryption struct {
	JWK json.RawMessage `json:"jwk"`
	Enc string          `json:"enc"`
	Zip string          `json:"zip,omitempty"`
}

// NewResponseEncrypter checks the wallet's response encryption parameters
// and returns the encrypter for its response (section 10). Compression is
// not supported. The JWE carries the kid of the wallet's key, if it has one.
func NewResponseEncrypter(p ResponseEncryption) (jose.Encrypter, error) {
	if p.Zip != "" {
		return nil, errors.New("compression is not supported")
	}
	enc := jose.ContentEncryption(p.Enc)
	if !containsMethod(enc) {
		return nil, errors.New("enc must be A128GCM or A256GCM")
	}
	var k jose.JSONWebKey
	if err := json.Unmarshal(p.JWK, &k); err != nil || !k.Valid() || !k.IsPublic() {
		return nil, errors.New("jwk must be a public key")
	}
	if pub, ok := k.Key.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("jwk must be a P-256 key")
	}
	if k.Use != "" && k.Use != "enc" {
		return nil, errors.New("jwk must be an encryption key")
	}
	// Section 10: the alg of the key must be present and is the JWE alg.
	if k.Algorithm != string(EncryptionAlgorithm) {
		return nil, errors.New("jwk must have alg ECDH-ES")
	}
	return jose.NewEncrypter(enc, jose.Recipient{Algorithm: EncryptionAlgorithm, Key: k.Key, KeyID: k.KeyID}, nil)
}

// EncryptResponse encrypts a JSON response body with an encrypter from
// NewResponseEncrypter; the result is served as application/jwt.
func EncryptResponse(e jose.Encrypter, body []byte) (string, error) {
	obj, err := e.Encrypt(body)
	if err != nil {
		return "", err
	}
	return obj.CompactSerialize()
}

// RequestKey is a key a wallet encrypts its credential request to,
// published in credential_request_encryption.jwks.
type RequestKey struct {
	KeyID string
	Key   *ecdsa.PrivateKey
}

// PublicJWK is the published form of the key.
func (k RequestKey) PublicJWK() jose.JSONWebKey {
	return jose.JSONWebKey{Key: k.Key.Public(), KeyID: k.KeyID, Algorithm: string(EncryptionAlgorithm), Use: "enc"}
}

// DecryptRequest decrypts a compact JWE credential request to one of keys,
// selected by its kid (section 10). A compressed request is refused.
func DecryptRequest(compact string, keys []RequestKey) ([]byte, error) {
	obj, err := jose.ParseEncryptedCompact(compact, []jose.KeyAlgorithm{EncryptionAlgorithm}, EncryptionMethods)
	if err != nil {
		return nil, errors.New("the request is not a JWE encrypted with ECDH-ES and A128GCM or A256GCM")
	}
	if _, ok := obj.Header.ExtraHeaders["zip"]; ok {
		return nil, errors.New("compression is not supported")
	}
	for _, k := range keys {
		if k.Key != nil && k.KeyID != "" && k.KeyID == obj.Header.KeyID {
			body, err := obj.Decrypt(k.Key)
			if err != nil {
				return nil, errors.New("the request does not decrypt")
			}
			return body, nil
		}
	}
	return nil, errors.New("the request is not encrypted to a key of this issuer")
}

func containsMethod(enc jose.ContentEncryption) bool {
	for _, m := range EncryptionMethods {
		if m == enc {
			return true
		}
	}
	return false
}
