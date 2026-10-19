// Package walletverifier connects the authorization server to the NL Wallet
// through nl-wallet's verification_server (slice 7 of #336). This file is
// the provision-wallet-verifier command: it issues the reader certificate of
// the usecase asi-login from the development reader CA (ca.gbo-reader, which
// the test wallet trusts), and writes the verifier's configuration and the
// API key of its internal API.
package walletverifier

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Usecase is the verifier's usecase of the ASI login; the wallet shows the
// purpose and organisation of its reader certificate.
const Usecase = "asi-login"

// PIDType is the NL PID the authorization server asks the wallet for.
const PIDType = "urn:eudi:pid:nl:1"

// PIDClaims are the claims requested from the PID: the BSN and the
// mandatory PID attributes the NL PID has. It has no place of birth.
var PIDClaims = []string{"bsn", "family_name", "given_name", "birthdate", "nationalities"}

var (
	readerAuthExtensionOID = asn1.ObjectIdentifier{2, 1, 123, 1}
	readerEKUOID           = asn1.ObjectIdentifier{1, 0, 18013, 5, 1, 6}
	extendedKeyUsageOID    = asn1.ObjectIdentifier{2, 5, 29, 37}
)

// Provision issues the reader certificate and writes, in the output
// directory, verification_server.toml (with the reader's private key), and
// the API key of the internal API to its own file: the authorization server
// gets only that file, not the verifier's configuration.
func Provision(args []string) error {
	fs := flag.NewFlagSet("provision-wallet-verifier", flag.ContinueOnError)
	caKeyPath := fs.String("ca-key", "", "PEM file with the development reader CA private key")
	caCertPath := fs.String("ca-cert", "", "PEM file with the development reader CA certificate")
	issuerCA := fs.String("issuer-ca", "", "PEM file with the development issuer CA (accepted as PID issuer)")
	pidAnchors := fs.String("pid-issuer-anchors", "", "file with further PID issuer trust anchors, base64 DER, one per line")
	publicURL := fs.String("public-url", "", "the verifier's public URL, which the wallet reaches; its host is the certificate's DNS SAN")
	origin := fs.String("request-origin", "", "the authorization server's public URL (requestOriginBaseUrl)")
	// The pre-production NL Wallet (TestFlight); the verifier appends
	// "disclosure". The EUDI route's QR uses the same base with
	// "disclosure_based_issuance" (EUDI_UL_BASE).
	universalLink := fs.String("universal-link-base", "https://app.preproductie.wallet.edi.bzk.nl/deeplink/", "base of the wallet's universal link")
	organization := fs.String("organization", "GBO demo ASIP (autorisatieserver)", "organisation the wallet shows")
	outDir := fs.String("out-dir", "", "directory for verification_server.toml")
	apiKeyFile := fs.String("api-key-file", "", "file for the API key of the internal API, which the authorization server reads (default: api-key in out-dir)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caKeyPath == "" || *caCertPath == "" || *issuerCA == "" || *publicURL == "" || *origin == "" || *outDir == "" {
		return errors.New("--ca-key, --ca-cert, --issuer-ca, --public-url, --request-origin and --out-dir are required")
	}
	pub, err := url.Parse(*publicURL)
	if err != nil || pub.Hostname() == "" {
		return fmt.Errorf("--public-url %q has no host", *publicURL)
	}
	caKey, caCert, err := loadCA(*caKeyPath, *caCertPath)
	if err != nil {
		return err
	}
	certDER, keyDER, err := issueReaderCertificate(caKey, caCert, pub.Hostname(), readerPayload(*organization, *origin))
	if err != nil {
		return err
	}
	issuerDER, err := pemCertificate(*issuerCA)
	if err != nil {
		return err
	}
	issuers := []string{base64.StdEncoding.EncodeToString(issuerDER)}
	if *pidAnchors != "" {
		more, err := base64Lines(*pidAnchors)
		if err != nil {
			return err
		}
		issuers = append(issuers, more...)
	}
	secret := make([]byte, 32)
	apiKey := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	if _, err := rand.Read(apiKey); err != nil {
		return err
	}
	config := Config{
		PublicURL:         strings.TrimRight(*publicURL, "/") + "/",
		UniversalLinkBase: *universalLink,
		IssuerAnchors:     issuers,
		ReaderAnchors:     []string{base64.StdEncoding.EncodeToString(caCert.Raw)},
		EphemeralIDSecret: hex.EncodeToString(secret),
		APIKey:            hex.EncodeToString(apiKey),
		ReaderKey:         base64.StdEncoding.EncodeToString(keyDER),
		ReaderCertificate: base64.StdEncoding.EncodeToString(certDER),
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "verification_server.toml"), []byte(config.TOML()), 0o600); err != nil {
		return err
	}
	if inOutDir := filepath.Join(*outDir, "api-key"); *apiKeyFile == "" {
		*apiKeyFile = inOutDir
	} else if err := os.Remove(inOutDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err // an API key of an earlier layout, next to the configuration
	}
	if err := os.MkdirAll(filepath.Dir(*apiKeyFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(*apiKeyFile, []byte(config.APIKey), 0o600); err != nil {
		return err
	}
	fmt.Printf("-> wallet verifier configuration written to %s (verification_server.toml), and its API key; reader certificate for %s\n", *outDir, pub.Hostname())
	return nil
}

// readerPayload is the reader authorisation metadata the wallet shows and
// enforces: who asks, why, and which PID attributes it may ask.
func readerPayload(organization, origin string) []byte {
	var authorized [][]string
	for _, c := range PIDClaims {
		authorized = append(authorized, []string{PIDType, c}, []string{c})
	}
	payload, _ := json.Marshal(map[string]any{
		"purposeStatement": map[string]string{
			"nl": "Inloggen bij de bron om een verzoek van een vertrouwensdienst goed te keuren",
			"en": "Log in at the source to approve a request of a trust service provider",
		},
		"retentionPolicy": map[string]any{"intentToRetain": false},
		"sharingPolicy":   map[string]any{"intentToShare": false},
		"deletionPolicy":  map[string]any{"deleteable": true},
		"organization": map[string]any{
			"displayName": map[string]string{"nl": organization, "en": organization},
			"legalName":   map[string]string{"nl": organization, "en": organization},
			"description": map[string]string{"nl": "Autorisatieserver van de ASI van de GBO-demo", "en": "Authorization server of the GBO demo's ASI"},
			"category":    map[string]string{"nl": "Overheid", "en": "Government"},
			"countryCode": "nl",
		},
		"requestOriginBaseUrl": origin,
		"authorizedAttributes": map[string]any{PIDType: authorized},
	})
	return payload
}

func issueReaderCertificate(caKey *ecdsa.PrivateKey, caCert *x509.Certificate, host string, payload []byte) (certDER, keyDER []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, nil, err
	}
	eku, err := asn1.Marshal([]asn1.ObjectIdentifier{readerEKUOID})
	if err != nil {
		return nil, nil, err
	}
	auth, err := asn1.Marshal(string(payload))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host, Organization: []string{"GBO demo"}, Country: []string{"NL"}},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-5 * time.Minute),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtraExtensions: []pkix.Extension{
			{Id: extendedKeyUsageOID, Critical: true, Value: eku},
			{Id: readerAuthExtensionOID, Value: auth},
		},
	}
	certDER, err = x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("issue reader certificate: %w", err)
	}
	keyDER, err = x509.MarshalPKCS8PrivateKey(key)
	return certDER, keyDER, err
}

func loadCA(keyPath, certPath string) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read CA key: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, errors.New("CA key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		k, ecErr := x509.ParseECPrivateKey(block.Bytes)
		if ecErr != nil {
			return nil, nil, fmt.Errorf("parse CA key: %w", err)
		}
		parsed = k
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("CA key is not ECDSA")
	}
	der, err := pemCertificate(certPath)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	return key, cert, nil
}

func pemCertificate(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s is not a PEM certificate", path)
	}
	return block.Bytes, nil
}

// base64Lines reads base64 DER certificates, one per line; # starts a comment.
func base64Lines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		der, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, err := x509.ParseCertificate(der); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, line)
	}
	return out, nil
}
