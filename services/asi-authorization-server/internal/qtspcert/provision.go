// Package qtspcert is the provision-qtsp-certificate command: it issues a
// test certificate for a QTSP from the development reader CA, the CA that
// issues the access certificates of the demo (the WRPAC role, ETSI
// TS 119 478 REQ-AZSP-6.1.3.2-02). With its key a test QTSP signs its
// software statement and its client assertions (REQ-TSP-6.1.3.2-01).
//
// It is NOT a Wallet-Relying Party Access Certificate according to ETSI
// TS 119 411-8; it only carries the organizationIdentifier the authorization
// server matches with the register.
package qtspcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

var oidOrganizationIdentifier = asn1.ObjectIdentifier{2, 5, 4, 97}

// Provision issues the certificate.
func Provision(args []string) error {
	fs := flag.NewFlagSet("provision-qtsp-certificate", flag.ContinueOnError)
	caKeyPath := fs.String("ca-key", "", "PEM file with the development reader CA private key")
	caCertPath := fs.String("ca-cert", "", "PEM file with the development reader CA certificate")
	outDir := fs.String("out-dir", "", "directory for qtsp-key.pem and qtsp-cert.pem")
	orgID := fs.String("organization-identifier", "NTRNL-90000001", "organizationIdentifier, as in the register entry")
	orgName := fs.String("organization", "GBO demo test QTSP", "organisation name in the certificate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caKeyPath == "" || *caCertPath == "" || *outDir == "" {
		return errors.New("--ca-key, --ca-cert and --out-dir are required")
	}
	caKey, caCert, err := loadCA(*caKeyPath, *caCertPath)
	if err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   *orgName + " (test, not a WRPAC)",
			Organization: []string{*orgName},
			Country:      []string{"NL"},
			ExtraNames:   []pkix.AttributeTypeAndValue{{Type: oidOrganizationIdentifier, Value: *orgID}},
		},
		NotBefore: time.Now().Add(-5 * time.Minute),
		NotAfter:  time.Now().AddDate(1, 0, 0),
		KeyUsage:  x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("issue QTSP certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		return err
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})...)
	if err := os.WriteFile(filepath.Join(*outDir, "qtsp-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "qtsp-cert.pem"), chain, 0o644); err != nil {
		return err
	}
	fmt.Printf("-> test QTSP certificate written to %s (qtsp-key.pem, qtsp-cert.pem)\n", *outDir)
	return nil
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
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read CA certificate: %w", err)
	}
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, nil, errors.New("CA certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	return key, cert, nil
}
