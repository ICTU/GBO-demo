package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const consentTokenType = "gbo-consent+jwt"

type ConsentClaims struct {
	ConsentID        string       `json:"consent_id"`
	PI               string       `json:"pi"`
	Scopes           []string     `json:"scopes"`
	ScopeEntries     []ScopeEntry `json:"scope_entries,omitempty"`
	DienstverlenrOIN string       `json:"dienstverlener_oin"`
	ValidUntil       string       `json:"valid_until"`
	jwt.RegisteredClaims
}

type ConsentIssuer struct {
	key      *ecdsa.PrivateKey
	kid      string
	issuer   string
	audience string
	now      func() time.Time
}

func NewConsentIssuer(cfg config) (*ConsentIssuer, error) {
	key, err := loadConsentSigningKey(cfg.SigningKeyPath)
	if err != nil {
		return nil, err
	}
	if cfg.SigningKeyID == "" || cfg.TokenIssuer == "" || cfg.TokenAudience == "" {
		return nil, fmt.Errorf("signing key id, issuer and audience are required")
	}
	return &ConsentIssuer{
		key: key, kid: cfg.SigningKeyID, issuer: cfg.TokenIssuer,
		audience: cfg.TokenAudience, now: time.Now,
	}, nil
}

func loadConsentSigningKey(path string) (*ecdsa.PrivateKey, error) {
	if path == "" {
		return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read consent signing key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("consent signing key is not PEM")
	}
	if value, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, ok := value.(*ecdsa.PrivateKey)
		if !ok || key.Curve != elliptic.P256() {
			return nil, fmt.Errorf("consent signing key must be ECDSA P-256")
		}
		return key, nil
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("parse consent signing key as ECDSA P-256")
	}
	return key, nil
}

func (i *ConsentIssuer) Sign(consent Consent, pi string) (string, error) {
	now := i.now().UTC()
	claims := ConsentClaims{
		ConsentID:        consent.ConsentID,
		PI:               pi,
		Scopes:           consent.Scopes,
		ScopeEntries:     consent.ScopeEntries,
		DienstverlenrOIN: consent.DienstverlenrOIN,
		ValidUntil:       consent.ValidUntil.UTC().Format(time.RFC3339),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    i.issuer,
			Audience:  jwt.ClaimStrings{i.audience},
			ExpiresAt: jwt.NewNumericDate(consent.ValidUntil),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        uuid.NewString(),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = i.kid
	token.Header["typ"] = consentTokenType
	return token.SignedString(i.key)
}

func (i *ConsentIssuer) JWKS() map[string]any {
	pub := i.key.PublicKey
	x := pub.X.FillBytes(make([]byte, 32))
	y := pub.Y.FillBytes(make([]byte, 32))
	return map[string]any{"keys": []map[string]string{{
		"kty": "EC",
		"crv": "P-256",
		"use": "sig",
		"alg": "ES256",
		"kid": i.kid,
		"x":   base64.RawURLEncoding.EncodeToString(x),
		"y":   base64.RawURLEncoding.EncodeToString(y),
	}}}
}
