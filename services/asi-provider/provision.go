package main

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

// oidOrganizationIdentifier is the X.520 organizationIdentifier attribute
// (2.5.4.97), which EN 319 412-1 uses for the registration number of the
// organisation in a seal certificate.
var oidOrganizationIdentifier = asn1.ObjectIdentifier{2, 5, 4, 97}

// provisionSealCertificate issues a development seal certificate for the
// ASIP from the pre-provisioned development issuer CA. It is NOT a qualified
// certificate for electronic seals: it only lets the mock seal its results
// and lets clients practise validating the seal.
func provisionSealCertificate(args []string) error {
	fs := flag.NewFlagSet("provision-seal-certificate", flag.ContinueOnError)
	caKeyPath := fs.String("ca-key", "", "PEM file with the development CA private key")
	caCertPath := fs.String("ca-cert", "", "PEM file with the development CA certificate")
	outDir := fs.String("out-dir", "", "directory for seal-key.pem and seal-cert.pem")
	orgID := fs.String("organization-identifier", "NTRNL-00000000000000000000", "organizationIdentifier of the sealing organisation")
	orgName := fs.String("organization", "GBO demo", "organisation name in the certificate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *caKeyPath == "" || *caCertPath == "" || *outDir == "" {
		return errors.New("--ca-key, --ca-cert and --out-dir are required")
	}
	caSigner, caCert, err := loadCA(*caKeyPath, *caCertPath)
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
			CommonName:   "GBO demo ASIP seal (development, not qualified)",
			Organization: []string{*orgName},
			Country:      []string{"NL"},
			ExtraNames:   []pkix.AttributeTypeAndValue{{Type: oidOrganizationIdentifier, Value: *orgID}},
		},
		NotBefore: time.Now().Add(-5 * time.Minute),
		NotAfter:  time.Now().AddDate(1, 0, 0),
		KeyUsage:  x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caSigner)
	if err != nil {
		return fmt.Errorf("issue seal certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		return err
	}
	keyOut := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	chainOut := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw})...)
	if err := os.WriteFile(filepath.Join(*outDir, "seal-key.pem"), keyOut, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "seal-cert.pem"), chainOut, 0o644); err != nil {
		return err
	}
	fmt.Printf("-> development seal certificate written to %s (seal-key.pem, seal-cert.pem)\n", *outDir)
	return nil
}

func loadCA(keyPath, certPath string) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	signer, chain, err := loadSealMaterial(keyPath, certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load development CA: %w", err)
	}
	key, ok := signer.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("development CA key is not ECDSA")
	}
	return key, chain[0], nil
}
