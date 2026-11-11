// Package nlconfig reads the NL Wallet configuration through a verified JWT
// or the explicitly enabled, official-HTTPS-only demo shortcut.
package nlconfig

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gbo-demo/eudi-adapter/internal/trustlists"
	"github.com/go-jose/go-jose/v4"
)

const DefaultURL = "https://static.preproductie.wallet.edi.bzk.nl/config/v1/wallet-config"

// DefaultPIDFingerprint identifies the NL non-production PID CA in config v49.
// A replacement requires an intentional operator configuration change.
const DefaultPIDFingerprint = "6dace11d226dbedec67eb1792d56f722d93ed0ce9501004aa3fa22d9f38c5b38"

type Source struct {
	URL                string
	Key                *ecdsa.PublicKey
	PIDFingerprints    []string
	Client             *http.Client
	TrustOfficialHTTPS bool
}

func (s Source) Load(ctx context.Context) (trustlists.Snapshot, error) {
	httpsDemo := s.Key == nil && s.TrustOfficialHTTPS
	if s.Key == nil && !httpsDemo {
		return trustlists.Snapshot{}, errors.New("a trusted NL pre-production ES256 public key or the explicit HTTPS demo shortcut is required")
	}
	if httpsDemo && s.URL != DefaultURL {
		return trustlists.Snapshot{}, errors.New("HTTPS demo trust is restricted to the fixed official NL pre-production URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL, nil)
	if err != nil {
		return trustlists.Snapshot{}, err
	}
	client := &http.Client{Timeout: 20 * time.Second}
	if s.Client != nil {
		copy := *s.Client
		client = &copy
	}
	if httpsDemo {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return errors.New("HTTPS demo trust does not allow redirects")
		}
	}
	response, err := client.Do(req)
	if err != nil {
		return trustlists.Snapshot{}, fmt.Errorf("fetch NL configuration: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return trustlists.Snapshot{}, fmt.Errorf("NL configuration: HTTP %d", response.StatusCode)
	}
	if httpsDemo && (response.TLS == nil || len(response.TLS.VerifiedChains) == 0) {
		return trustlists.Snapshot{}, errors.New("HTTPS demo trust requires verified TLS certificates")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return trustlists.Snapshot{}, err
	}
	if len(body) > 2<<20 {
		return trustlists.Snapshot{}, errors.New("NL configuration exceeds 2 MiB")
	}
	if !httpsDemo {
		return s.Verify(body)
	}
	jwt, err := jose.ParseSignedCompact(strings.TrimSpace(string(body)), []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return trustlists.Snapshot{}, fmt.Errorf("parse HTTPS-trusted NL configuration: %w", err)
	}
	// Demo shortcut: only this Load path, after fixed-URL and TLS checks, may
	// use the payload without verifying its JWT signature. Verify stays strict.
	return s.decode(jwt.UnsafePayloadWithoutVerification(), trustlists.NLHTTPSDemo)
}

func (s Source) Verify(raw []byte) (trustlists.Snapshot, error) {
	var snapshot trustlists.Snapshot
	if s.Key == nil || s.Key.Curve != elliptic.P256() {
		return snapshot, errors.New("trusted NL ES256 public key is required")
	}
	jwt, err := jose.ParseSignedCompact(strings.TrimSpace(string(raw)), []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return snapshot, fmt.Errorf("parse NL configuration: %w", err)
	}
	body, err := jwt.Verify(s.Key)
	if err != nil {
		return snapshot, fmt.Errorf("verify NL configuration signature: %w", err)
	}
	return s.decode(body, trustlists.NLJWTVerified)
}

func (s Source) decode(body []byte, authentication trustlists.NLAuthentication) (trustlists.Snapshot, error) {
	var snapshot trustlists.Snapshot
	var config struct {
		Version     uint64   `json:"version"`
		Environment string   `json:"environment"`
		Issuers     []string `json:"issuer_trust_anchors"`
		WRPAC       []string `json:"wrpac_trust_anchors"`
		WRPRC       []string `json:"wrprc_trust_anchors"`
		Disclosure  struct {
			Readers []string `json:"rp_trust_anchors"`
			WRPAC   []string `json:"wrpac_trust_anchors"`
			WRPRC   []string `json:"wrprc_trust_anchors"`
		} `json:"disclosure"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return snapshot, fmt.Errorf("decode NL configuration: %w", err)
	}
	if config.Environment != "preproductie" || config.Version == 0 || len(config.Issuers) == 0 {
		return snapshot, errors.New("NL configuration must identify preproductie, a positive version and issuer anchors")
	}
	snapshot = trustlists.Snapshot{NL: map[trustlists.Role][]trustlists.Anchor{}, NLVersion: config.Version, NLSource: s.URL, NLAuthentication: authentication}
	origin := fmt.Sprintf("%s#version=%d&authentication=%s", s.URL, config.Version, authentication)
	pins := map[string]bool{}
	for _, pin := range s.PIDFingerprints {
		pin = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(pin), ":", ""))
		decoded, err := hex.DecodeString(pin)
		if err != nil || len(decoded) != 32 {
			return snapshot, errors.New("NL PID CA fingerprints must contain 64 SHA-256 hex digits")
		}
		pins[pin] = true
	}
	if len(pins) == 0 {
		return snapshot, errors.New("at least one NL PID CA fingerprint is required")
	}
	add := func(encoded string, role trustlists.Role) error {
		der, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return fmt.Errorf("NL anchor: %w", err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil || !cert.IsCA {
			return errors.New("NL anchor is not a CA certificate")
		}
		snapshot.NL[role] = append(snapshot.NL[role], trustlists.Anchor{DER: der, Origins: []string{origin}})
		return nil
	}
	for _, encoded := range config.Issuers {
		der, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return snapshot, err
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return snapshot, err
		}
		role := trustlists.PubEAA
		if pins[trustlists.Fingerprint(der)] {
			role = trustlists.PID
		} else if cert.Subject.CommonName == "issuance-ca.nonprod.wallet.edi.bzk.nl" {
			return snapshot, errors.New("NL PID CA changed: review its fingerprint before publication")
		}
		if err := add(encoded, role); err != nil {
			return snapshot, err
		}
	}
	if len(snapshot.NL[trustlists.PID]) == 0 {
		return snapshot, errors.New("pinned NL PID CA is missing from issuer anchors")
	}
	readers := append(config.Disclosure.Readers, config.WRPAC...)
	readers = append(readers, config.Disclosure.WRPAC...)
	if len(readers) == 0 {
		return snapshot, errors.New("NL configuration has no relying-party access anchors")
	}
	for _, encoded := range readers {
		if err := add(encoded, trustlists.WRPAC); err != nil {
			return snapshot, err
		}
	}
	for _, encoded := range append(config.WRPRC, config.Disclosure.WRPRC...) {
		if err := add(encoded, trustlists.WRPRC); err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}

// ReadKey accepts a public-key PEM, base64 SubjectPublicKeyInfo DER, or the
// pre-production wallet build's config-server-config.json. Never a JWT's key.
func ReadKey(path string) (*ecdsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read trusted NL configuration public key: %w", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		var config struct {
			Environment string `json:"environment"`
			Key         string `json:"signing_public_key"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, err
		}
		if config.Environment != "preproductie" {
			return nil, errors.New("NL configuration key file must belong to preproductie")
		}
		data = []byte(config.Key)
	}
	var der []byte
	if block, rest := pem.Decode(data); block != nil {
		if block.Type != "PUBLIC KEY" || len(strings.TrimSpace(string(rest))) != 0 {
			return nil, errors.New("expected exactly one PUBLIC KEY PEM block")
		}
		der = block.Bytes
	} else {
		der, err = base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, err
		}
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("NL configuration key must be an ES256 public key")
	}
	return ec, nil
}
