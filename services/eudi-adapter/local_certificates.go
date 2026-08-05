package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

var (
	issuerAuthExtensionOID = asn1.ObjectIdentifier{2, 1, 123, 2}
	readerAuthExtensionOID = asn1.ObjectIdentifier{2, 1, 123, 1}
	issuerEKUOID           = asn1.ObjectIdentifier{1, 0, 18013, 5, 1, 2}
	readerEKUOID           = asn1.ObjectIdentifier{1, 0, 18013, 5, 1, 6}
	statusEKUOID           = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 127}
)

type certificateArtifacts struct {
	IssuerKeyPath      string `json:"issuer_key_path"`
	IssuerCertPath     string `json:"issuer_cert_path"`
	ReaderKeyPath      string `json:"reader_key_path"`
	ReaderCertPath     string `json:"reader_cert_path"`
	StatusKeyPath      string `json:"status_key_path"`
	StatusCertPath     string `json:"status_cert_path"`
	IssuerCACertPath   string `json:"issuer_ca_cert_path"`
	ReaderCACertPath   string `json:"reader_ca_cert_path"`
	IssuerSubject      string `json:"issuer_subject"`
	ReaderSubject      string `json:"reader_subject"`
	CertificateExpires string `json:"certificate_expires_at"`
}

type certificateProvider interface {
	Provision(sourceRegistration) (certificateArtifacts, error)
}

type localCertificateProvider struct {
	root         string
	random       io.Reader
	now          func() time.Time
	readerOrigin string
}

type localCA struct {
	key      *ecdsa.PrivateKey
	cert     *x509.Certificate
	certPath string
}

func newLocalCertificateProvider(root, readerOrigin string) *localCertificateProvider {
	return &localCertificateProvider{root: root, random: rand.Reader, now: time.Now, readerOrigin: readerOrigin}
}

func (p *localCertificateProvider) Provision(registration sourceRegistration) (certificateArtifacts, error) {
	if p == nil || p.root == "" {
		return certificateArtifacts{}, fmt.Errorf("local certificate secret directory is required")
	}
	if err := ensurePrivateDirectory(p.root); err != nil {
		return certificateArtifacts{}, err
	}
	caDir := filepath.Join(p.root, "development-ca")
	issuerCA, err := p.ensureCA(caDir, "issuer")
	if err != nil {
		return certificateArtifacts{}, err
	}
	readerCA, err := p.ensureCA(caDir, "reader")
	if err != nil {
		return certificateArtifacts{}, err
	}
	sourceDir := filepath.Join(p.root, registration.SourceOIN)
	if err := ensurePrivateDirectory(sourceDir); err != nil {
		return certificateArtifacts{}, err
	}
	issuerHost := "issuer-" + registration.SourceOIN + ".gbo.local"
	readerHost := "reader-" + registration.SourceOIN + ".gbo.local"
	readerOrigin := "https://" + readerHost + "/"
	if p.readerOrigin != "" {
		parsed, err := url.Parse(p.readerOrigin)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return certificateArtifacts{}, fmt.Errorf("local reader origin must be an HTTPS origin without credentials, path, query or fragment")
		}
		parsed.Path = "/"
		readerOrigin = parsed.String()
		readerHost = parsed.Hostname()
	}
	issuerSubject := pkix.Name{CommonName: issuerHost, Organization: []string{registration.Name}, SerialNumber: registration.SourceOIN}
	readerSubject := pkix.Name{CommonName: readerHost, Organization: []string{registration.Name}, SerialNumber: registration.SourceOIN}
	issuerPayload, readerPayload, err := localCertificateAuthPayloads(registration, readerOrigin)
	if err != nil {
		return certificateArtifacts{}, err
	}
	issuer, err := p.ensureLeaf(sourceDir, "issuer", issuerSubject, issuerHost, issuerCA, issuerEKUOID, issuerAuthExtensionOID, issuerPayload)
	if err != nil {
		return certificateArtifacts{}, err
	}
	reader, err := p.ensureLeaf(sourceDir, "reader", readerSubject, readerHost, readerCA, readerEKUOID, readerAuthExtensionOID, readerPayload)
	if err != nil {
		return certificateArtifacts{}, err
	}
	status, err := p.ensureLeaf(sourceDir, "status", issuerSubject, issuerHost, issuerCA, statusEKUOID, nil, nil)
	if err != nil {
		return certificateArtifacts{}, err
	}
	if err := validateProvisionedCertificate(issuer.cert, issuerCA.cert, registration, issuerEKUOID); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate issuer certificate: %w", err)
	}
	if err := validateProvisionedCertificate(reader.cert, readerCA.cert, registration, readerEKUOID); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate reader certificate: %w", err)
	}
	if err := validateProvisionedCertificate(status.cert, issuerCA.cert, registration, statusEKUOID); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate status certificate: %w", err)
	}
	if issuer.cert.Subject.String() != status.cert.Subject.String() {
		return certificateArtifacts{}, fmt.Errorf("issuer and status certificate subjects differ")
	}
	return certificateArtifacts{
		IssuerKeyPath:      issuer.keyPath,
		IssuerCertPath:     issuer.certPath,
		ReaderKeyPath:      reader.keyPath,
		ReaderCertPath:     reader.certPath,
		StatusKeyPath:      status.keyPath,
		StatusCertPath:     status.certPath,
		IssuerCACertPath:   issuerCA.certPath,
		ReaderCACertPath:   readerCA.certPath,
		IssuerSubject:      issuer.cert.Subject.String(),
		ReaderSubject:      reader.cert.Subject.String(),
		CertificateExpires: issuer.cert.NotAfter.UTC().Format(time.RFC3339),
	}, nil
}

type localLeaf struct {
	keyPath  string
	certPath string
	cert     *x509.Certificate
}

func (p *localCertificateProvider) ensureCA(directory, role string) (*localCA, error) {
	if err := ensurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(directory, role+"-ca-key.pem")
	certPath := filepath.Join(directory, role+"-ca-cert.pem")
	keyExists, certExists := fileExists(keyPath), fileExists(certPath)
	if keyExists != certExists {
		return nil, fmt.Errorf("local %s CA is partially provisioned", role)
	}
	if keyExists {
		key, cert, err := loadLocalCA(keyPath, certPath)
		if err != nil {
			return nil, fmt.Errorf("load local %s CA: %w", role, err)
		}
		return &localCA{key: key, cert: cert, certPath: certPath}, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), p.random)
	if err != nil {
		return nil, fmt.Errorf("generate local %s CA key: %w", role, err)
	}
	now := p.now().UTC()
	serial, err := randomSerial(p.random)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "GBO local " + role + " development CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(5, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(p.random, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create local %s CA certificate: %w", role, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal local %s CA key: %w", role, err)
	}
	if err := writeFileAtomically(directory, filepath.Base(keyPath), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, err
	}
	if err := writeFileAtomically(directory, filepath.Base(certPath), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse generated local %s CA certificate: %w", role, err)
	}
	return &localCA{key: key, cert: cert, certPath: certPath}, nil
}

func (p *localCertificateProvider) ensureLeaf(directory, role string, subject pkix.Name, host string, ca *localCA, eku, authOID asn1.ObjectIdentifier, authPayload []byte) (*localLeaf, error) {
	keyPath := filepath.Join(directory, role+"-key.der.b64")
	certPath := filepath.Join(directory, role+"-cert.der.b64")
	keyExists, certExists := fileExists(keyPath), fileExists(certPath)
	if keyExists != certExists {
		return nil, fmt.Errorf("local %s certificate is partially provisioned", role)
	}
	if keyExists {
		key, cert, err := loadLocalLeaf(keyPath, certPath)
		if err != nil {
			return nil, fmt.Errorf("load local %s certificate: %w", role, err)
		}
		if !publicKeysEqual(&key.PublicKey, cert.PublicKey) {
			return nil, fmt.Errorf("local %s key does not match its certificate", role)
		}
		return &localLeaf{keyPath: keyPath, certPath: certPath, cert: cert}, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), p.random)
	if err != nil {
		return nil, fmt.Errorf("generate local %s key: %w", role, err)
	}
	serial, err := randomSerial(p.random)
	if err != nil {
		return nil, err
	}
	now := p.now().UTC()
	template := &x509.Certificate{
		SerialNumber:       serial,
		Subject:            subject,
		DNSNames:           []string{host},
		NotBefore:          now.Add(-5 * time.Minute),
		NotAfter:           now.AddDate(1, 0, 0),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		UnknownExtKeyUsage: []asn1.ObjectIdentifier{eku},
	}
	if len(authOID) > 0 {
		encoded, err := asn1.Marshal(string(authPayload))
		if err != nil {
			return nil, fmt.Errorf("encode local %s authorization metadata: %w", role, err)
		}
		template.ExtraExtensions = []pkix.Extension{{Id: authOID, Value: encoded}}
	}
	certDER, err := x509.CreateCertificate(p.random, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("create local %s certificate: %w", role, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal local %s key: %w", role, err)
	}
	if err := writeFileAtomically(directory, filepath.Base(keyPath), []byte(base64.StdEncoding.EncodeToString(keyDER)), 0o600); err != nil {
		return nil, err
	}
	if err := writeFileAtomically(directory, filepath.Base(certPath), []byte(base64.StdEncoding.EncodeToString(certDER)), 0o644); err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse generated local %s certificate: %w", role, err)
	}
	return &localLeaf{keyPath: keyPath, certPath: certPath, cert: cert}, nil
}

func localCertificateAuthPayloads(registration sourceRegistration, readerOrigin string) ([]byte, []byte, error) {
	organization := map[string]any{
		"displayName": map[string]string{"nl": registration.Name, "en": registration.Name},
		"legalName":   map[string]string{"nl": registration.Name, "en": registration.Name},
		"description": map[string]string{"nl": "Lokale ontwikkelbron " + registration.Name, "en": "Local development source " + registration.Name},
		"category":    map[string]string{"nl": "Overheid", "en": "Government"},
		"countryCode": "nl",
	}
	issuer, err := json.Marshal(map[string]any{"organization": organization})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal local issuer authorization metadata: %w", err)
	}
	reader, err := json.Marshal(map[string]any{
		"organization":         organization,
		"requestOriginBaseUrl": readerOrigin,
		"authorizedAttributes": map[string]any{"urn:eudi:pid:nl:1": [][]string{{"urn:eudi:pid:nl:1", "bsn"}, {"bsn"}}},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal local reader authorization metadata: %w", err)
	}
	return issuer, reader, nil
}

func validateProvisionedCertificate(cert, ca *x509.Certificate, registration sourceRegistration, eku asn1.ObjectIdentifier) error {
	if cert.Subject.SerialNumber != registration.SourceOIN {
		return fmt.Errorf("certificate is not bound to source OIN %q", registration.SourceOIN)
	}
	if len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] != registration.Name {
		return fmt.Errorf("certificate is not bound to source name %q", registration.Name)
	}
	if !containsOID(cert.UnknownExtKeyUsage, eku) {
		return fmt.Errorf("certificate does not contain required EKU %s", eku.String())
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: time.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return fmt.Errorf("certificate chain is not trusted: %w", err)
	}
	return nil
}

func loadLocalCA(keyPath, certPath string) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("CA key is not PEM")
	}
	keyValue, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, ok := keyValue.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("CA key is not ECDSA")
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("CA certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if !publicKeysEqual(&key.PublicKey, cert.PublicKey) {
		return nil, nil, fmt.Errorf("CA key does not match its certificate")
	}
	return key, cert, nil
}

func loadLocalLeaf(keyPath, certPath string) (*ecdsa.PrivateKey, *x509.Certificate, error) {
	encodedKey, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(encodedKey)))
	if err != nil {
		return nil, nil, err
	}
	keyValue, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, nil, err
	}
	key, ok := keyValue.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("leaf key is not ECDSA")
	}
	encodedCert, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	certDER, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(encodedCert)))
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(certDER)
	return key, cert, err
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure private directory %q: %w", path, err)
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func randomSerial(random io.Reader) (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(random, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return serial, nil
}

func publicKeysEqual(left *ecdsa.PublicKey, right any) bool {
	other, ok := right.(*ecdsa.PublicKey)
	return ok && left.Equal(other)
}

func containsOID(values []asn1.ObjectIdentifier, want asn1.ObjectIdentifier) bool {
	for _, value := range values {
		if value.Equal(want) {
			return true
		}
	}
	return false
}
