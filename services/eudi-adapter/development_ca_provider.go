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
	"reflect"
	"time"
)

var (
	issuerAuthExtensionOID = asn1.ObjectIdentifier{2, 1, 123, 2}
	readerAuthExtensionOID = asn1.ObjectIdentifier{2, 1, 123, 1}
	issuerEKUOID           = asn1.ObjectIdentifier{1, 0, 18013, 5, 1, 2}
	readerEKUOID           = asn1.ObjectIdentifier{1, 0, 18013, 5, 1, 6}
	statusEKUOID           = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 127}
	extendedKeyUsageOID    = asn1.ObjectIdentifier{2, 5, 29, 37}
)

type certificateArtifacts struct {
	IssuerKeyReference    string `json:"issuer_key_reference"`
	IssuerCertReference   string `json:"issuer_cert_reference"`
	ReaderKeyReference    string `json:"reader_key_reference"`
	ReaderCertReference   string `json:"reader_cert_reference"`
	StatusKeyReference    string `json:"status_key_reference"`
	StatusCertReference   string `json:"status_cert_reference"`
	IssuerCACertReference string `json:"issuer_ca_cert_reference"`
	ReaderCACertReference string `json:"reader_ca_cert_reference"`
	IssuerSubject         string `json:"issuer_subject"`
	ReaderSubject         string `json:"reader_subject"`
	CertificateExpires    string `json:"certificate_expires_at"`
	sourceOIN             string
	sourceName            string
}

type certificateProvider interface {
	Provision(sourceRegistration) (certificateArtifacts, error)
}

type certificateStore interface {
	Load(sourceRegistration) (certificateArtifacts, error)
}

type developmentCAProvider struct {
	root            string
	random          io.Reader
	now             func() time.Time
	readerPublicURL string
}

type developmentCA struct {
	key      *ecdsa.PrivateKey
	cert     *x509.Certificate
	certPath string
}

func newDevelopmentCAProvider(root, readerPublicURL string) *developmentCAProvider {
	return &developmentCAProvider{root: root, random: rand.Reader, now: time.Now, readerPublicURL: readerPublicURL}
}

type developmentCertificateBinding struct {
	issuerHost    string
	readerHost    string
	issuerSubject pkix.Name
	readerSubject pkix.Name
	issuerPayload []byte
	readerPayload []byte
}

func (p *developmentCAProvider) binding(registration sourceRegistration) (developmentCertificateBinding, error) {
	issuerHost := "issuer-" + registration.SourceOIN + ".gbo.local"
	readerHost := "reader-" + registration.SourceOIN + ".gbo.local"
	if p.readerPublicURL != "" {
		parsed, err := parseDevelopmentRootURL(p.readerPublicURL, false)
		if err != nil {
			return developmentCertificateBinding{}, fmt.Errorf("development reader public URL: %w", err)
		}
		readerHost = parsed.Hostname()
	}
	readerOrigin := "https://" + readerHost + "/"
	issuerSubject := pkix.Name{CommonName: issuerHost, Organization: []string{registration.Name}, SerialNumber: registration.SourceOIN}
	readerSubject := pkix.Name{CommonName: readerHost, Organization: []string{registration.Name}, SerialNumber: registration.SourceOIN}
	issuerPayload, readerPayload, err := developmentCertificateAuthPayloads(registration, readerOrigin)
	if err != nil {
		return developmentCertificateBinding{}, err
	}
	return developmentCertificateBinding{
		issuerHost: issuerHost, readerHost: readerHost,
		issuerSubject: issuerSubject, readerSubject: readerSubject,
		issuerPayload: issuerPayload, readerPayload: readerPayload,
	}, nil
}

func (p *developmentCAProvider) Provision(registration sourceRegistration) (certificateArtifacts, error) {
	if p == nil || p.root == "" {
		return certificateArtifacts{}, fmt.Errorf("development CA secret directory is required")
	}
	if err := ensurePrivateDirectory(p.root); err != nil {
		return certificateArtifacts{}, err
	}
	caDir := filepath.Join(p.root, "development-ca")
	issuerCA, err := p.loadPreProvisionedCA(caDir, "issuer")
	if err != nil {
		return certificateArtifacts{}, err
	}
	readerCA, err := p.loadPreProvisionedCA(caDir, "reader")
	if err != nil {
		return certificateArtifacts{}, err
	}
	sourceDir := filepath.Join(p.root, registration.certificateSetID())
	if err := ensurePrivateDirectory(sourceDir); err != nil {
		return certificateArtifacts{}, err
	}
	binding, err := p.binding(registration)
	if err != nil {
		return certificateArtifacts{}, err
	}
	issuer, err := p.ensureLeaf(sourceDir, "issuer", binding.issuerSubject, binding.issuerHost, issuerCA, issuerEKUOID, issuerAuthExtensionOID, binding.issuerPayload)
	if err != nil {
		return certificateArtifacts{}, err
	}
	reader, err := p.ensureLeaf(sourceDir, "reader", binding.readerSubject, binding.readerHost, readerCA, readerEKUOID, readerAuthExtensionOID, binding.readerPayload)
	if err != nil {
		return certificateArtifacts{}, err
	}
	status, err := p.ensureLeaf(sourceDir, "status", binding.issuerSubject, binding.issuerHost, issuerCA, statusEKUOID, nil, nil)
	if err != nil {
		return certificateArtifacts{}, err
	}
	now := p.now()
	if err := validateProvisionedCertificate(issuer.cert, issuerCA.cert, registration, issuerEKUOID, now); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate issuer certificate: %w", err)
	}
	if err := validateProvisionedCertificate(reader.cert, readerCA.cert, registration, readerEKUOID, now); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate reader certificate: %w", err)
	}
	if err := validateProvisionedCertificate(status.cert, issuerCA.cert, registration, statusEKUOID, now); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate status certificate: %w", err)
	}
	if issuer.cert.Subject.String() != status.cert.Subject.String() {
		return certificateArtifacts{}, fmt.Errorf("issuer and status certificate subjects differ")
	}
	return certificateArtifacts{
		IssuerKeyReference:    issuer.keyPath,
		IssuerCertReference:   issuer.certPath,
		ReaderKeyReference:    reader.keyPath,
		ReaderCertReference:   reader.certPath,
		StatusKeyReference:    status.keyPath,
		StatusCertReference:   status.certPath,
		IssuerCACertReference: issuerCA.certPath,
		ReaderCACertReference: readerCA.certPath,
		IssuerSubject:         issuer.cert.Subject.String(),
		ReaderSubject:         reader.cert.Subject.String(),
		CertificateExpires:    issuer.cert.NotAfter.UTC().Format(time.RFC3339),
		sourceOIN:             registration.SourceOIN,
		sourceName:            registration.Name,
	}, nil
}

// Load reads and validates certificates that were provisioned explicitly. It
// never creates directories, keys or certificates, so onboarding cannot mint
// certificate material as a side effect.
func (p *developmentCAProvider) Load(registration sourceRegistration) (certificateArtifacts, error) {
	if p == nil || p.root == "" {
		return certificateArtifacts{}, fmt.Errorf("development CA secret directory is required")
	}
	caDir := filepath.Join(p.root, "development-ca")
	issuerCACert := filepath.Join(caDir, "issuer-ca-cert.pem")
	readerCACert := filepath.Join(caDir, "reader-ca-cert.pem")
	issuerCA, err := loadDevelopmentCACertificate(issuerCACert)
	if err != nil {
		return certificateArtifacts{}, fmt.Errorf("load explicitly provisioned development issuer CA certificate: %w", err)
	}
	readerCA, err := loadDevelopmentCACertificate(readerCACert)
	if err != nil {
		return certificateArtifacts{}, fmt.Errorf("load explicitly provisioned development reader CA certificate: %w", err)
	}
	sourceDir := filepath.Join(p.root, registration.certificateSetID())
	loadLeaf := func(role string) (*developmentLeaf, error) {
		keyPath := filepath.Join(sourceDir, role+"-key.der.b64")
		certPath := filepath.Join(sourceDir, role+"-cert.der.b64")
		key, cert, err := loadDevelopmentLeaf(keyPath, certPath)
		if err != nil {
			return nil, fmt.Errorf("load explicitly provisioned development %s certificate: %w", role, err)
		}
		if !publicKeysEqual(&key.PublicKey, cert.PublicKey) {
			return nil, fmt.Errorf("development %s key does not match its certificate", role)
		}
		return &developmentLeaf{keyPath: keyPath, certPath: certPath, cert: cert}, nil
	}
	issuer, err := loadLeaf("issuer")
	if err != nil {
		return certificateArtifacts{}, err
	}
	reader, err := loadLeaf("reader")
	if err != nil {
		return certificateArtifacts{}, err
	}
	status, err := loadLeaf("status")
	if err != nil {
		return certificateArtifacts{}, err
	}
	identityOIN, identityName, err := certificateSourceIdentity(issuer.cert)
	if err != nil {
		return certificateArtifacts{}, fmt.Errorf("read explicitly provisioned source identity: %w", err)
	}
	if registration.SourceOIN != "" && registration.SourceOIN != identityOIN {
		return certificateArtifacts{}, fmt.Errorf("certificate source OIN %q does not match configured OIN %q", identityOIN, registration.SourceOIN)
	}
	if registration.Name != "" && registration.Name != identityName {
		return certificateArtifacts{}, fmt.Errorf("certificate source name %q does not match configured name %q", identityName, registration.Name)
	}
	registration.SourceOIN = identityOIN
	registration.Name = identityName
	binding, err := p.binding(registration)
	if err != nil {
		return certificateArtifacts{}, err
	}
	now := p.now()
	if err := validateExpectedDevelopmentLeaf(issuer.cert, issuerCA, binding.issuerSubject, binding.issuerHost, issuerEKUOID, issuerAuthExtensionOID, binding.issuerPayload, now); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate explicitly provisioned issuer certificate: %w", err)
	}
	if err := validateExpectedDevelopmentLeaf(reader.cert, readerCA, binding.readerSubject, binding.readerHost, readerEKUOID, readerAuthExtensionOID, binding.readerPayload, now); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate explicitly provisioned reader certificate: %w", err)
	}
	if err := validateExpectedDevelopmentLeaf(status.cert, issuerCA, binding.issuerSubject, binding.issuerHost, statusEKUOID, nil, nil, now); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate explicitly provisioned status certificate: %w", err)
	}
	return certificateArtifacts{
		IssuerKeyReference: issuer.keyPath, IssuerCertReference: issuer.certPath,
		ReaderKeyReference: reader.keyPath, ReaderCertReference: reader.certPath,
		StatusKeyReference: status.keyPath, StatusCertReference: status.certPath,
		IssuerCACertReference: issuerCACert, ReaderCACertReference: readerCACert,
		IssuerSubject: issuer.cert.Subject.String(), ReaderSubject: reader.cert.Subject.String(),
		CertificateExpires: issuer.cert.NotAfter.UTC().Format(time.RFC3339),
		sourceOIN:          identityOIN, sourceName: identityName,
	}, nil
}

func certificateSourceIdentity(cert *x509.Certificate) (string, string, error) {
	if cert == nil || !sourceOINPattern.MatchString(cert.Subject.SerialNumber) {
		return "", "", fmt.Errorf("issuer certificate serialNumber must contain the 20-digit source OIN")
	}
	if len(cert.Subject.Organization) != 1 || cert.Subject.Organization[0] == "" {
		return "", "", fmt.Errorf("issuer certificate must contain exactly one organisation name")
	}
	return cert.Subject.SerialNumber, cert.Subject.Organization[0], nil
}

func projectPublicCertificateArtifacts(root, sourceID string, artifacts certificateArtifacts) error {
	if root == "" {
		return fmt.Errorf("public certificate projection directory is required")
	}
	if !sourceIDPattern.MatchString(sourceID) {
		return fmt.Errorf("public certificate projection source_id is invalid")
	}
	files := []struct {
		directory string
		name      string
		source    string
	}{
		{filepath.Join(root, "development-ca"), "issuer-ca-cert.pem", artifacts.IssuerCACertReference},
		{filepath.Join(root, "development-ca"), "reader-ca-cert.pem", artifacts.ReaderCACertReference},
		{filepath.Join(root, sourceID), "issuer-cert.der.b64", artifacts.IssuerCertReference},
		{filepath.Join(root, sourceID), "reader-cert.der.b64", artifacts.ReaderCertReference},
		{filepath.Join(root, sourceID), "status-cert.der.b64", artifacts.StatusCertReference},
	}
	for _, file := range files {
		if file.source == "" {
			return fmt.Errorf("public certificate %s source path is missing", file.name)
		}
		body, err := os.ReadFile(file.source)
		if err != nil {
			return fmt.Errorf("read public certificate %s: %w", file.name, err)
		}
		if err := os.MkdirAll(file.directory, 0o755); err != nil {
			return fmt.Errorf("create public certificate directory: %w", err)
		}
		if err := writeFileAtomically(file.directory, file.name, body, 0o644); err != nil {
			return fmt.Errorf("write public certificate %s: %w", file.name, err)
		}
	}
	return nil
}

// publicDevelopmentCertificateStore validates the same certificate chains as
// the development provider without opening any private-key file. It is the
// only certificate-store mode permitted for registry-backed reconciliation.
type publicDevelopmentCertificateStore struct {
	provider *developmentCAProvider
}

func newPublicDevelopmentCertificateStore(root, readerPublicURL string) *publicDevelopmentCertificateStore {
	return &publicDevelopmentCertificateStore{provider: newDevelopmentCAProvider(root, readerPublicURL)}
}

func (s *publicDevelopmentCertificateStore) Load(registration sourceRegistration) (certificateArtifacts, error) {
	if s == nil || s.provider == nil || s.provider.root == "" {
		return certificateArtifacts{}, fmt.Errorf("development certificate directory is required")
	}
	caDir := filepath.Join(s.provider.root, "development-ca")
	issuerCACertPath := filepath.Join(caDir, "issuer-ca-cert.pem")
	readerCACertPath := filepath.Join(caDir, "reader-ca-cert.pem")
	issuerCA, err := loadDevelopmentCACertificate(issuerCACertPath)
	if err != nil {
		return certificateArtifacts{}, fmt.Errorf("load explicitly provisioned development issuer CA certificate: %w", err)
	}
	readerCA, err := loadDevelopmentCACertificate(readerCACertPath)
	if err != nil {
		return certificateArtifacts{}, fmt.Errorf("load explicitly provisioned development reader CA certificate: %w", err)
	}
	sourceDir := filepath.Join(s.provider.root, registration.certificateSetID())
	loadLeaf := func(role string) (*x509.Certificate, string, error) {
		path := filepath.Join(sourceDir, role+"-cert.der.b64")
		certificate, err := loadDevelopmentLeafCertificate(path)
		if err != nil {
			return nil, "", fmt.Errorf("load explicitly provisioned development %s certificate: %w", role, err)
		}
		return certificate, path, nil
	}
	issuer, issuerPath, err := loadLeaf("issuer")
	if err != nil {
		return certificateArtifacts{}, err
	}
	reader, readerPath, err := loadLeaf("reader")
	if err != nil {
		return certificateArtifacts{}, err
	}
	status, statusPath, err := loadLeaf("status")
	if err != nil {
		return certificateArtifacts{}, err
	}
	identityOIN, identityName, err := certificateSourceIdentity(issuer)
	if err != nil {
		return certificateArtifacts{}, fmt.Errorf("read explicitly provisioned source identity: %w", err)
	}
	if registration.SourceOIN != "" && registration.SourceOIN != identityOIN {
		return certificateArtifacts{}, fmt.Errorf("certificate source OIN %q does not match configured OIN %q", identityOIN, registration.SourceOIN)
	}
	if registration.Name != "" && registration.Name != identityName {
		return certificateArtifacts{}, fmt.Errorf("certificate source name %q does not match configured name %q", identityName, registration.Name)
	}
	registration.SourceOIN = identityOIN
	registration.Name = identityName
	binding, err := s.provider.binding(registration)
	if err != nil {
		return certificateArtifacts{}, err
	}
	now := s.provider.now()
	if err := validateExpectedDevelopmentLeaf(issuer, issuerCA, binding.issuerSubject, binding.issuerHost, issuerEKUOID, issuerAuthExtensionOID, binding.issuerPayload, now); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate explicitly provisioned issuer certificate: %w", err)
	}
	if err := validateExpectedDevelopmentLeaf(reader, readerCA, binding.readerSubject, binding.readerHost, readerEKUOID, readerAuthExtensionOID, binding.readerPayload, now); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate explicitly provisioned reader certificate: %w", err)
	}
	if err := validateExpectedDevelopmentLeaf(status, issuerCA, binding.issuerSubject, binding.issuerHost, statusEKUOID, nil, nil, now); err != nil {
		return certificateArtifacts{}, fmt.Errorf("validate explicitly provisioned status certificate: %w", err)
	}
	return certificateArtifacts{
		IssuerCertReference: issuerPath, ReaderCertReference: readerPath, StatusCertReference: statusPath,
		IssuerCACertReference: issuerCACertPath, ReaderCACertReference: readerCACertPath,
		IssuerSubject: issuer.Subject.String(), ReaderSubject: reader.Subject.String(),
		CertificateExpires: issuer.NotAfter.UTC().Format(time.RFC3339),
		sourceOIN:          identityOIN, sourceName: identityName,
	}, nil
}

type developmentLeaf struct {
	keyPath  string
	certPath string
	cert     *x509.Certificate
}

func (p *developmentCAProvider) loadPreProvisionedCA(directory, role string) (*developmentCA, error) {
	keyPath := filepath.Join(directory, role+"-ca-key.pem")
	certPath := filepath.Join(directory, role+"-ca-cert.pem")
	keyExists, certExists := fileExists(keyPath), fileExists(certPath)
	if !keyExists || !certExists {
		return nil, fmt.Errorf("pre-provisioned development %s CA key and certificate are required at %q and %q", role, keyPath, certPath)
	}
	key, cert, err := loadDevelopmentCA(keyPath, certPath)
	if err != nil {
		return nil, fmt.Errorf("load pre-provisioned development %s CA: %w", role, err)
	}
	return &developmentCA{key: key, cert: cert, certPath: certPath}, nil
}

func (p *developmentCAProvider) ensureLeaf(directory, role string, subject pkix.Name, host string, ca *developmentCA, eku, authOID asn1.ObjectIdentifier, authPayload []byte) (*developmentLeaf, error) {
	keyPath := filepath.Join(directory, role+"-key.der.b64")
	certPath := filepath.Join(directory, role+"-cert.der.b64")
	keyExists, certExists := fileExists(keyPath), fileExists(certPath)
	if keyExists != certExists {
		return nil, fmt.Errorf("development %s certificate is partially provisioned", role)
	}
	var key *ecdsa.PrivateKey
	if keyExists {
		loadedKey, cert, err := loadDevelopmentLeaf(keyPath, certPath)
		if err != nil {
			return nil, fmt.Errorf("load development %s certificate: %w", role, err)
		}
		if !publicKeysEqual(&loadedKey.PublicKey, cert.PublicKey) {
			return nil, fmt.Errorf("development %s key does not match its certificate", role)
		}
		if err := validateExpectedDevelopmentLeaf(cert, ca.cert, subject, host, eku, authOID, authPayload, p.now()); err == nil {
			return &developmentLeaf{keyPath: keyPath, certPath: certPath, cert: cert}, nil
		}
		// Reuse the bound private key while replacing stale certificate content.
		// This keeps key references stable and makes configuration changes explicit
		// in the newly issued certificate.
		key = loadedKey
	} else {
		var err error
		key, err = ecdsa.GenerateKey(elliptic.P256(), p.random)
		if err != nil {
			return nil, fmt.Errorf("generate development %s key: %w", role, err)
		}
	}
	serial, err := randomSerial(p.random)
	if err != nil {
		return nil, err
	}
	now := p.now().UTC()
	ekuValue, err := asn1.Marshal([]asn1.ObjectIdentifier{eku})
	if err != nil {
		return nil, fmt.Errorf("encode development %s EKU: %w", role, err)
	}
	template := &x509.Certificate{
		SerialNumber:    serial,
		Subject:         subject,
		DNSNames:        []string{host},
		NotBefore:       now.Add(-5 * time.Minute),
		NotAfter:        now.AddDate(1, 0, 0),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		ExtraExtensions: []pkix.Extension{{Id: extendedKeyUsageOID, Critical: true, Value: ekuValue}},
	}
	if len(authOID) > 0 {
		encoded, err := asn1.Marshal(string(authPayload))
		if err != nil {
			return nil, fmt.Errorf("encode development %s authorization metadata: %w", role, err)
		}
		template.ExtraExtensions = append(template.ExtraExtensions, pkix.Extension{Id: authOID, Value: encoded})
	}
	certDER, err := x509.CreateCertificate(p.random, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, fmt.Errorf("create development %s certificate: %w", role, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal development %s key: %w", role, err)
	}
	if !keyExists {
		if err := writeFileAtomically(directory, filepath.Base(keyPath), []byte(base64.StdEncoding.EncodeToString(keyDER)), 0o600); err != nil {
			return nil, err
		}
	}
	if err := writeFileAtomically(directory, filepath.Base(certPath), []byte(base64.StdEncoding.EncodeToString(certDER)), 0o644); err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("parse generated development %s certificate: %w", role, err)
	}
	return &developmentLeaf{keyPath: keyPath, certPath: certPath, cert: cert}, nil
}

func developmentCertificateAuthPayloads(registration sourceRegistration, readerOrigin string) ([]byte, []byte, error) {
	organization := map[string]any{
		"displayName": map[string]string{"nl": registration.Name, "en": registration.Name},
		"legalName":   map[string]string{"nl": registration.Name, "en": registration.Name},
		"description": map[string]string{"nl": "Lokale ontwikkelbron " + registration.Name, "en": "Local development source " + registration.Name},
		"category":    map[string]string{"nl": "Overheid", "en": "Government"},
		"countryCode": "nl",
	}
	if registration.Logo != nil {
		organization["logo"] = registration.Logo
	}
	issuer, err := json.Marshal(map[string]any{"organization": organization})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal development issuer authorization metadata: %w", err)
	}
	reader, err := json.Marshal(map[string]any{
		"purposeStatement": map[string]string{
			"nl": "Uitgifte van attestaties van " + registration.Name,
			"en": "Issuance of attestations by " + registration.Name,
		},
		"retentionPolicy": map[string]any{
			"intentToRetain": false,
		},
		"sharingPolicy": map[string]any{
			"intentToShare": false,
		},
		"deletionPolicy": map[string]any{
			"deleteable": true,
		},
		"organization":         organization,
		"requestOriginBaseUrl": readerOrigin,
		"authorizedAttributes": map[string]any{"urn:eudi:pid:nl:1": [][]string{{"urn:eudi:pid:nl:1", "bsn"}, {"bsn"}}},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("marshal development reader authorization metadata: %w", err)
	}
	return issuer, reader, nil
}

func validateProvisionedCertificate(cert, ca *x509.Certificate, registration sourceRegistration, eku asn1.ObjectIdentifier, now time.Time) error {
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
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return fmt.Errorf("certificate chain is not trusted: %w", err)
	}
	return nil
}

func parseDevelopmentRootURL(value string, requireHTTPS bool) (*url.URL, error) {
	parsed, err := url.Parse(value)
	validScheme := parsed != nil && (parsed.Scheme == "https" || (!requireHTTPS && parsed.Scheme == "http"))
	if err != nil || !validScheme || parsed.Hostname() == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		scheme := "HTTP(S)"
		if requireHTTPS {
			scheme = "HTTPS"
		}
		return nil, fmt.Errorf("must be a root %s URL without credentials, query or fragment", scheme)
	}
	return parsed, nil
}

func validateExpectedDevelopmentLeaf(cert, ca *x509.Certificate, subject pkix.Name, host string, eku, authOID asn1.ObjectIdentifier, authPayload []byte, now time.Time) error {
	if cert.Subject.String() != subject.String() {
		return fmt.Errorf("certificate subject does not match current source registration")
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != host {
		return fmt.Errorf("certificate DNS SAN does not match %q", host)
	}
	ekuExtension, ok := findCertificateExtension(cert, extendedKeyUsageOID)
	if !ok || !ekuExtension.Critical || !containsOID(cert.UnknownExtKeyUsage, eku) {
		return fmt.Errorf("certificate does not contain the required critical EKU %s", eku.String())
	}
	if len(authOID) > 0 {
		authExtension, ok := findCertificateExtension(cert, authOID)
		if !ok || authExtension.Critical {
			return fmt.Errorf("certificate authorization extension %s is missing or critical", authOID.String())
		}
		var encodedPayload string
		remaining, err := asn1.Unmarshal(authExtension.Value, &encodedPayload)
		if err != nil || len(remaining) != 0 || !authorizationPayloadMatches([]byte(encodedPayload), authPayload) {
			return fmt.Errorf("certificate authorization extension %s does not match current configuration", authOID.String())
		}
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return fmt.Errorf("certificate chain is not trusted: %w", err)
	}
	return nil
}

// authorizationPayloadMatches permits an explicitly provisioned organization
// logo to remain certificate-owned when the reconciler later loads the source
// without provisioning input. Every other authorization field must match.
func authorizationPayloadMatches(actual, expected []byte) bool {
	var actualValue, expectedValue map[string]any
	if json.Unmarshal(actual, &actualValue) != nil || json.Unmarshal(expected, &expectedValue) != nil {
		return false
	}
	expectedOrganization, expectedHasOrganization := expectedValue["organization"].(map[string]any)
	actualOrganization, actualHasOrganization := actualValue["organization"].(map[string]any)
	if expectedHasOrganization && actualHasOrganization {
		if _, expectsLogo := expectedOrganization["logo"]; !expectsLogo {
			delete(actualOrganization, "logo")
		}
	}
	return reflect.DeepEqual(actualValue, expectedValue)
}

func findCertificateExtension(cert *x509.Certificate, oid asn1.ObjectIdentifier) (pkix.Extension, bool) {
	for _, extension := range cert.Extensions {
		if extension.Id.Equal(oid) {
			return extension, true
		}
	}
	return pkix.Extension{}, false
}

func loadDevelopmentCA(keyPath, certPath string) (*ecdsa.PrivateKey, *x509.Certificate, error) {
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
	cert, err := loadDevelopmentCACertificate(certPath)
	if err != nil {
		return nil, nil, err
	}
	if !publicKeysEqual(&key.PublicKey, cert.PublicKey) {
		return nil, nil, fmt.Errorf("CA key does not match its certificate")
	}
	return key, cert, nil
}

func loadDevelopmentCACertificate(certPath string) (*x509.Certificate, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, fmt.Errorf("CA certificate is not PEM")
	}
	return x509.ParseCertificate(certBlock.Bytes)
}

func loadDevelopmentLeaf(keyPath, certPath string) (*ecdsa.PrivateKey, *x509.Certificate, error) {
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

func loadDevelopmentLeafCertificate(certPath string) (*x509.Certificate, error) {
	encodedCert, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	certDER, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(encodedCert)))
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(certDER)
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
