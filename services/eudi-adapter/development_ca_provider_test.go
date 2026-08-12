package main

import (
	"bytes"
	"crypto/x509"
	"encoding/asn1"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestDevelopmentCAProviderBindsReaderCertificateToCurrentConfiguration(t *testing.T) {
	registration := sourceRegistration{
		SourceOIN: "99999999900000000200",
		Name:      "Belastingdienst-mock",
	}
	fixedNow := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	provider := newDevelopmentCAProvider(t.TempDir(), "https://issuance.example")
	provider.now = func() time.Time { return fixedNow }

	first, err := provider.Provision(registration)
	if err != nil {
		t.Fatalf("provision first certificate set: %v", err)
	}
	firstReader := loadCertificateArtifact(t, first.ReaderKeyReference, first.ReaderCertReference)
	assertCriticalEKU(t, firstReader, readerEKUOID)
	if len(firstReader.DNSNames) != 1 || firstReader.DNSNames[0] != "issuance.example" {
		t.Fatalf("reader DNS SANs = %v, want [issuance.example]", firstReader.DNSNames)
	}
	if got := readerRequestOrigin(t, firstReader); got != "https://issuance.example/" {
		t.Fatalf("reader request origin = %q", got)
	}
	assertReaderAuthorizationPolicies(t, firstReader)
	for name, artifact := range map[string]struct {
		keyPath  string
		certPath string
		eku      asn1.ObjectIdentifier
	}{
		"issuer": {first.IssuerKeyReference, first.IssuerCertReference, issuerEKUOID},
		"status": {first.StatusKeyReference, first.StatusCertReference, statusEKUOID},
	} {
		t.Run(name+" critical EKU", func(t *testing.T) {
			assertCriticalEKU(t, loadCertificateArtifact(t, artifact.keyPath, artifact.certPath), artifact.eku)
		})
	}
	firstReaderBytes, err := os.ReadFile(first.ReaderCertReference)
	if err != nil {
		t.Fatal(err)
	}
	firstReaderKey, err := os.ReadFile(first.ReaderKeyReference)
	if err != nil {
		t.Fatal(err)
	}

	provider.readerPublicURL = "https://new-issuance.example"
	second, err := provider.Provision(registration)
	if err != nil {
		t.Fatalf("reprovision changed reader public URL: %v", err)
	}
	secondReaderBytes, err := os.ReadFile(second.ReaderCertReference)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstReaderBytes, secondReaderBytes) {
		t.Fatal("reader certificate was reused after the reader public URL changed")
	}
	secondReader := loadCertificateArtifact(t, second.ReaderKeyReference, second.ReaderCertReference)
	if len(secondReader.DNSNames) != 1 || secondReader.DNSNames[0] != "new-issuance.example" {
		t.Fatalf("reissued reader DNS SANs = %v, want [new-issuance.example]", secondReader.DNSNames)
	}
	if got := readerRequestOrigin(t, secondReader); got != "https://new-issuance.example/" {
		t.Fatalf("reissued reader request origin = %q", got)
	}
	secondReaderKey, err := os.ReadFile(second.ReaderKeyReference)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstReaderKey, secondReaderKey) {
		t.Fatal("reader private key changed while refreshing only the certificate")
	}

	provider.now = func() time.Time { return fixedNow.AddDate(2, 0, 0) }
	if _, err := provider.Provision(registration); err != nil {
		t.Fatalf("reprovision expired leaf certificates using injected clock: %v", err)
	}
}

func loadCertificateArtifact(t *testing.T, keyPath, certPath string) *x509.Certificate {
	t.Helper()
	_, cert, err := loadDevelopmentLeaf(keyPath, certPath)
	if err != nil {
		t.Fatalf("load certificate artifact: %v", err)
	}
	return cert
}

func assertCriticalEKU(t *testing.T, cert *x509.Certificate, eku asn1.ObjectIdentifier) {
	t.Helper()
	extension, ok := findCertificateExtension(cert, extendedKeyUsageOID)
	if !ok || !extension.Critical {
		t.Fatalf("certificate EKU extension = %+v, present=%t; want critical", extension, ok)
	}
	if !containsOID(cert.UnknownExtKeyUsage, eku) {
		t.Fatalf("certificate EKUs = %v, want %s", cert.UnknownExtKeyUsage, eku.String())
	}
}

func readerRequestOrigin(t *testing.T, cert *x509.Certificate) string {
	t.Helper()
	payload := readerAuthorizationPayload(t, cert)
	requestOrigin, ok := payload["requestOriginBaseUrl"].(string)
	if !ok {
		t.Fatalf("reader requestOriginBaseUrl = %#v, want string", payload["requestOriginBaseUrl"])
	}
	return requestOrigin
}

func assertReaderAuthorizationPolicies(t *testing.T, cert *x509.Certificate) {
	t.Helper()
	payload := readerAuthorizationPayload(t, cert)
	purpose, ok := payload["purposeStatement"].(map[string]any)
	if !ok || purpose["nl"] == "" || purpose["en"] == "" {
		t.Fatalf("reader purposeStatement = %#v, want nl and en values", payload["purposeStatement"])
	}
	for field, child := range map[string]string{
		"retentionPolicy": "intentToRetain",
		"sharingPolicy":   "intentToShare",
	} {
		policy, ok := payload[field].(map[string]any)
		if !ok || policy[child] != false {
			t.Fatalf("reader %s = %#v, want %s=false", field, payload[field], child)
		}
	}
	deletion, ok := payload["deletionPolicy"].(map[string]any)
	if !ok || deletion["deleteable"] != true {
		t.Fatalf("reader deletionPolicy = %#v, want deleteable=true", payload["deletionPolicy"])
	}
}

func readerAuthorizationPayload(t *testing.T, cert *x509.Certificate) map[string]any {
	t.Helper()
	extension, ok := findCertificateExtension(cert, readerAuthExtensionOID)
	if !ok {
		t.Fatal("reader certificate has no authorization extension")
	}
	var encodedPayload string
	remaining, err := asn1.Unmarshal(extension.Value, &encodedPayload)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("decode reader authorization extension: remaining=%x err=%v", remaining, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(encodedPayload), &payload); err != nil {
		t.Fatalf("parse reader authorization payload: %v", err)
	}
	return payload
}
