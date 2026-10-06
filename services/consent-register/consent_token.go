package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const consentTokenType = "gbo-consent+jwt"

// EncryptedValue is one value BSNk made for a party and for one version of
// that party's keys. Only that party can decrypt it.
type EncryptedValue struct {
	KeySetVersion int    `json:"key_set_version"`
	Value         string `json:"value"`
}

// EncryptedSubject is the citizen as one party may read them. Every party
// has a pseudonym: a value that decrypts to the party's own pseudonym of the
// citizen. A party on the BSN authorisation list also has an identity: a
// value that decrypts to the BSN.
type EncryptedSubject struct {
	Identity  *EncryptedValue `json:"identity,omitempty"`
	Pseudonym *EncryptedValue `json:"pseudonym"`
}

var partyOIN = regexp.MustCompile(`^[0-9]{20}$`)

// validateEncryptedSubject checks the shape of what the token is about to
// carry. Whether a value is genuine is not the register's to tell: BSNk
// signed it, and the party it is for checks that when it decrypts. Who may
// have an identity is not the register's to tell either: BSNk makes one only
// for a party on its list.
func validateEncryptedSubject(subject map[string]EncryptedSubject) error {
	if len(subject) == 0 {
		return errors.New("encrypted_subject is required (no plain BSN accepted)")
	}
	for oin, s := range subject {
		if !partyOIN.MatchString(oin) {
			return fmt.Errorf("encrypted_subject: %q is not an OIN of 20 digits", oin)
		}
		if s.Pseudonym == nil {
			return fmt.Errorf("encrypted_subject[%s]: every party has a pseudonym", oin)
		}
		if !s.Pseudonym.complete() {
			return fmt.Errorf("encrypted_subject[%s].pseudonym: key_set_version and value are required", oin)
		}
		if s.Identity != nil && !s.Identity.complete() {
			return fmt.Errorf("encrypted_subject[%s].identity: key_set_version and value are required", oin)
		}
	}
	return nil
}

func (v EncryptedValue) complete() bool {
	return v.KeySetVersion > 0 && v.Value != ""
}

type ConsentClaims struct {
	ConsentID string `json:"consent_id"`
	// EncryptedSubject is keyed by the OIN of the party a value is for. The
	// token names the citizen in no other way.
	EncryptedSubject map[string]EncryptedSubject `json:"encrypted_subject"`
	Scopes           []string                    `json:"scopes"`
	ScopeEntries     []ScopeEntry                `json:"scope_entries,omitempty"`
	DienstverlenrOIN string                      `json:"dienstverlener_oin"`
	ValidUntil       string                      `json:"valid_until"`
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

func (i *ConsentIssuer) Sign(consent Consent, subject map[string]EncryptedSubject) (string, error) {
	now := i.now().UTC()
	claims := ConsentClaims{
		ConsentID:        consent.ConsentID,
		EncryptedSubject: subject,
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
