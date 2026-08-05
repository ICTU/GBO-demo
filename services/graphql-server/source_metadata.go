package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
)

var sourceMetadataBase64URL = base64.RawURLEncoding

type sourceMetadataPrivateJWK struct {
	KTY string `json:"kty"`
	CRV string `json:"crv"`
	X   string `json:"x"`
	D   string `json:"d"`
}

type sourceMetadataPublisher struct {
	compactJWS []byte
	etag       string
}

// newSourceMetadataPublisher validates the source-owned payload and private
// JWK once at startup, then signs an immutable compact JWS for publication.
// In production this handler is an internal backend of the source's dedicated
// FSC metadata service; it is not an internet-public endpoint.
func newSourceMetadataPublisher(payload, rawPrivateJWK []byte) (*sourceMetadataPublisher, error) {
	if !json.Valid(payload) {
		return nil, fmt.Errorf("source metadata payload is not valid JSON")
	}
	var jwk sourceMetadataPrivateJWK
	if err := json.Unmarshal(rawPrivateJWK, &jwk); err != nil {
		return nil, fmt.Errorf("parse source metadata signing JWK: %w", err)
	}
	if jwk.KTY != "OKP" || jwk.CRV != "Ed25519" {
		return nil, fmt.Errorf("source metadata signing JWK must be an Ed25519 OKP key")
	}
	seed, err := sourceMetadataBase64URL.DecodeString(jwk.D)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("source metadata signing JWK has an invalid private key")
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	x := sourceMetadataBase64URL.EncodeToString(publicKey)
	if jwk.X != x {
		return nil, fmt.Errorf("source metadata signing JWK public and private key do not match")
	}
	thumbprintInput := fmt.Sprintf(`{"crv":"Ed25519","kty":"OKP","x":"%s"}`, x)
	thumbprint := sha256.Sum256([]byte(thumbprintInput))
	protected, err := json.Marshal(map[string]any{
		"alg": "EdDSA",
		"kid": sourceMetadataBase64URL.EncodeToString(thumbprint[:]),
		"typ": "gbo-attestations+jws",
		"jwk": map[string]string{
			"kty": "OKP",
			"crv": "Ed25519",
			"x":   x,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal source metadata JWS header: %w", err)
	}
	signingInput := sourceMetadataBase64URL.EncodeToString(protected) + "." + sourceMetadataBase64URL.EncodeToString(payload)
	signature := ed25519.Sign(privateKey, []byte(signingInput))
	compact := signingInput + "." + sourceMetadataBase64URL.EncodeToString(signature)
	digest := sha256.Sum256([]byte(compact))
	return &sourceMetadataPublisher{
		compactJWS: []byte(compact),
		etag:       `"` + fmt.Sprintf("%x", digest) + `"`,
	}, nil
}

func (p *sourceMetadataPublisher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/jose")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", p.etag)
	if r.Header.Get("If-None-Match") == p.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(p.compactJWS)
}
