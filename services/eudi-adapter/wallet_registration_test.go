package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/eudi-adapter/internal/onboarding"
	"gbo-demo/oid4vc-kit/statuslist"
)

const (
	testTrustURL = "https://trust.example"
	testIncome   = "https://adapter.example/types/belastingdienst/inkomensverklaring/v1.0"
)

type walletRegistrationFixture struct {
	options walletRegistrationOptions
	source  walletRegistrationSource
	now     time.Time
}

// newWalletRegistrationFixture provisions the existing issuer certificate of
// one source, a reader root shaped like the pre-provisioned one (path length
// zero, no key usage extension) and the registrar root.
func newWalletRegistrationFixture(t *testing.T) walletRegistrationFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	secrets := t.TempDir()
	writeTestDevelopmentCAs(t, secrets, now)
	provider := newDevelopmentCAProvider(secrets, "https://issuer.example")
	provider.now = func() time.Time { return now }
	registration := sourceRegistration{SourceID: "belastingdienst", SourceOIN: "99999999900000000200", Name: "Belastingdienst"}
	if _, err := provider.Provision(registration); err != nil {
		t.Fatal(err)
	}
	writePreProvisionedReaderRoot(t, filepath.Join(secrets, "development-ca"), now)
	if err := createDevelopmentRegistrarRoot(filepath.Join(secrets, "development-ca"), now, rand.Reader); err != nil {
		t.Fatal(err)
	}
	return walletRegistrationFixture{
		options: walletRegistrationOptions{
			secretsDir: secrets, publishDir: t.TempDir(), trustPublicURL: testTrustURL,
			organizationIdentifierType: "XYZ", now: func() time.Time { return now }, random: rand.Reader,
		},
		source: walletRegistrationSource{SourceID: "belastingdienst", OIN: "99999999900000000200", Name: "Belastingdienst",
			CertificateSet: "belastingdienst", VCTs: []string{testIncome}},
		now: now,
	}
}

func writePreProvisionedReaderRoot(t *testing.T, caDir string, now time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "ca.gbo-reader"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0),
		BasicConstraintsValid: true, IsCA: true, MaxPathLenZero: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyPath, certPath := caFiles(caDir, "reader")
	if err := writeKeyAndCertificate(caDir, keyPath, certPath, key, [][]byte{der}); err != nil {
		t.Fatal(err)
	}
}

func (f walletRegistrationFixture) caCert(t *testing.T, role string) *x509.Certificate {
	t.Helper()
	_, certPath := caFiles(filepath.Join(f.options.secretsDir, "development-ca"), role)
	cert, err := loadDevelopmentCACertificate(certPath)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func (f walletRegistrationFixture) wrpac(t *testing.T) issuedCertificate {
	t.Helper()
	dir := filepath.Join(f.options.secretsDir, "belastingdienst")
	wrpac, err := readKeyAndChain(filepath.Join(dir, "wrpac-key.pem"), filepath.Join(dir, "wrpac-chain.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return wrpac
}

// verifiedJWT checks a compact JWS against the first x5c certificate,
// which must chain to root, and returns its header certificate and claims.
func verifiedJWT(t *testing.T, compact string, root *x509.Certificate) (jose.Header, *x509.Certificate, map[string]any) {
	t.Helper()
	jws, err := jose.ParseSigned(compact, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		t.Fatal(err)
	}
	header := jws.Signatures[0].Protected
	chain, err := header.Certificates(x509.VerifyOptions{Roots: certPool(root), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil {
		t.Fatalf("x5c does not chain to %s: %v", root.Subject, err)
	}
	body, err := jws.Verify(chain[0][0].PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatal(err)
	}
	return header, chain[0][0], claims
}

func certPool(c *x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c)
	return p
}

func TestProvisionWalletRegistration(t *testing.T) {
	f := newWalletRegistrationFixture(t)
	if err := provisionWalletRegistration(f.options, f.source); err != nil {
		t.Fatal(err)
	}
	reader, registrar := f.caCert(t, "reader"), f.caCert(t, registrarRootRole)

	t.Run("the WRPAC names the source under the reader root", func(t *testing.T) {
		wrpac := f.wrpac(t).chain
		if len(wrpac) != 1 || wrpac[0].CheckSignatureFrom(reader) != nil {
			t.Fatal("the WRPAC must be issued directly by the reader root, which allows no intermediate")
		}
		if got := organizationIdentifier(wrpac[0]); got != "XYZNL-99999999900000000200" {
			t.Fatalf("organizationIdentifier %q", got)
		}
		if !slices.Equal(wrpac[0].Subject.Country, []string{"NL"}) || !slices.Equal(wrpac[0].Subject.Organization, []string{"Belastingdienst"}) {
			t.Fatalf("subject %s", wrpac[0].Subject)
		}
		if !slices.Equal(wrpac[0].CRLDistributionPoints, []string{testTrustURL + "/reader-root.crl"}) {
			t.Fatalf("CRL distribution points %v", wrpac[0].CRLDistributionPoints)
		}
	})

	t.Run("the WRPRC is signed by the registrar and bound to the WRPAC", func(t *testing.T) {
		assertRegistrationCertificate(t, f, registrar)
	})

	t.Run("the CRL of the reader root revokes nothing", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(f.options.publishDir, "reader-root.crl"))
		if err != nil {
			t.Fatal(err)
		}
		crl, err := x509.ParseRevocationList(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := crl.CheckSignatureFrom(reader); err != nil {
			t.Fatal(err)
		}
		// The wallet matches the CRL to the WRPAC's issuer by name and key
		// identifier, and needs a next update.
		wrpac := f.wrpac(t).chain[0]
		if !bytes.Equal(crl.RawIssuer, wrpac.RawIssuer) || !bytes.Equal(crl.AuthorityKeyId, reader.SubjectKeyId) {
			t.Fatal("the CRL issuer must be the WRPAC's issuer")
		}
		for _, ext := range crl.Extensions {
			if ext.Id.Equal(asn1.ObjectIdentifier{2, 5, 29, 28}) {
				t.Fatal("an issuing distribution point would make the CRL partial")
			}
		}
		if len(crl.RevokedCertificateEntries) != 0 || !crl.NextUpdate.After(f.now.AddDate(0, 11, 0)) {
			t.Fatalf("revoked %d, next update %s", len(crl.RevokedCertificateEntries), crl.NextUpdate)
		}
	})

	t.Run("the WRPRC status list is signed under the registrar's subject", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(f.options.publishDir, "wrprc", "1"))
		if err != nil {
			t.Fatal(err)
		}
		header, cert, claims := verifiedJWT(t, string(raw), registrar)
		rc, err := os.ReadFile(filepath.Join(f.options.secretsDir, "belastingdienst", "wrprc.jwt"))
		if err != nil {
			t.Fatal(err)
		}
		_, signer, _ := verifiedJWT(t, string(rc), registrar)
		if !slices.ContainsFunc(cert.UnknownExtKeyUsage, statusEKUOID.Equal) || !bytes.Equal(cert.RawSubject, signer.RawSubject) {
			t.Fatalf("status signer %s, EKU %v, WRPRC signer %s", cert.Subject, cert.UnknownExtKeyUsage, signer.Subject)
		}
		if header.ExtraHeaders["typ"] != "statuslist+jwt" || claims["sub"] != testTrustURL+"/wrprc/1" || claims["exp"] == nil {
			t.Fatalf("header %v, claims %v", header.ExtraHeaders, claims)
		}
		bits, err := statuslist.Decode(claims["status_list"].(map[string]any)["lst"].(string))
		if err != nil || statuslist.Revoked(bits, 0) {
			t.Fatalf("the registration must be valid: %v", err)
		}
	})

	t.Run("the registrar root is only created on request", func(t *testing.T) {
		if err := createDevelopmentRegistrarRoot(filepath.Join(f.options.secretsDir, "development-ca"), f.now, rand.Reader); err == nil {
			t.Fatal("an existing registrar root must not be replaced")
		}
	})
}

func TestProvisionWalletRegistrationAgain(t *testing.T) {
	f := newWalletRegistrationFixture(t)
	if err := provisionWalletRegistration(f.options, f.source); err != nil {
		t.Fatal(err)
	}
	caDir := filepath.Join(f.options.secretsDir, "development-ca")
	first := f.wrpac(t)
	signer := readTestRole(t, caDir, "wrprc-signer")

	t.Run("a second run keeps keys, certificates and status index", func(t *testing.T) {
		if err := provisionWalletRegistration(f.options, f.source); err != nil {
			t.Fatal(err)
		}
		again := f.wrpac(t)
		if !again.key.Equal(first.key) || !again.chain[0].Equal(first.chain[0]) {
			t.Fatal("an unchanged WRPAC must be kept")
		}
		if !readTestRole(t, caDir, "wrprc-signer").chain[0].Equal(signer.chain[0]) {
			t.Fatal("the WRPRC signer must be kept")
		}
		if state, err := readWalletRegistrationState(caDir); err != nil || state.StatusIndices["belastingdienst"] != 0 {
			t.Fatalf("state %+v, %v", state, err)
		}
	})
	t.Run("another source gets one past the highest status index", func(t *testing.T) {
		state := walletRegistrationRecord{StatusIndices: map[string]int{"a": 0, "c": 2}}
		if idx, err := state.statusIndex("d"); err != nil || idx != 3 {
			t.Fatalf("index %d, %v", idx, err)
		}
	})
	t.Run("a new trust URL is refused while sources carry the old one", func(t *testing.T) {
		f.options.trustPublicURL = "https://other-trust.example"
		if err := provisionWalletRegistration(f.options, f.source); err == nil || !strings.Contains(err.Error(), "provision every source again") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("after a reset the WRPAC and WRPRC carry the new trust URL, same key", func(t *testing.T) {
		if err := os.Remove(filepath.Join(caDir, walletRegistrationState)); err != nil {
			t.Fatal(err)
		}
		if err := provisionWalletRegistration(f.options, f.source); err != nil {
			t.Fatal(err)
		}
		again := f.wrpac(t)
		if !again.key.Equal(first.key) || again.chain[0].CRLDistributionPoints[0] != "https://other-trust.example/reader-root.crl" {
			t.Fatalf("CRL distribution points %v", again.chain[0].CRLDistributionPoints)
		}
		rc, err := os.ReadFile(filepath.Join(f.options.secretsDir, "belastingdienst", "wrprc.jwt"))
		if err != nil {
			t.Fatal(err)
		}
		_, _, claims := verifiedJWT(t, string(rc), f.caCert(t, registrarRootRole))
		if claims["status"].(map[string]any)["uri"] != "https://other-trust.example/wrprc/1" {
			t.Fatalf("status %v", claims["status"])
		}
	})
	t.Run("a key left without certificate is replaced", func(t *testing.T) {
		dir := filepath.Join(f.options.secretsDir, "belastingdienst")
		if err := os.Remove(filepath.Join(dir, "wrpac-chain.pem")); err != nil {
			t.Fatal(err)
		}
		if err := provisionWalletRegistration(f.options, f.source); err != nil {
			t.Fatal(err)
		}
		if f.wrpac(t).key.Equal(first.key) {
			t.Fatal("a key whose certificate was never written must not be reused")
		}
	})
}

// A root that expires soon is refused, and nothing outlives the
// certificates it rests on.
func TestWalletRegistrationFollowsItsRoots(t *testing.T) {
	t.Run("a reader root expiring within a month is refused", func(t *testing.T) {
		f := newWalletRegistrationFixture(t)
		writePreProvisionedReaderRoot(t, filepath.Join(f.options.secretsDir, "development-ca"), f.now.AddDate(-1, 0, 20))
		if err := provisionWalletRegistration(f.options, f.source); err == nil || !strings.Contains(err.Error(), "renew it first") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a reader root expiring in two months caps WRPAC, WRPRC and CRL", func(t *testing.T) {
		f := newWalletRegistrationFixture(t)
		writePreProvisionedReaderRoot(t, filepath.Join(f.options.secretsDir, "development-ca"), f.now.AddDate(-1, 2, 0))
		if err := provisionWalletRegistration(f.options, f.source); err != nil {
			t.Fatal(err)
		}
		reader := f.caCert(t, "reader")
		if f.wrpac(t).chain[0].NotAfter.After(reader.NotAfter) {
			t.Fatal("the WRPAC outlives the reader root")
		}
		rc, err := os.ReadFile(filepath.Join(f.options.secretsDir, "belastingdienst", "wrprc.jwt"))
		if err != nil {
			t.Fatal(err)
		}
		_, _, claims := verifiedJWT(t, string(rc), f.caCert(t, registrarRootRole))
		if time.Unix(int64(claims["exp"].(float64)), 0).After(f.wrpac(t).chain[0].NotAfter) {
			t.Fatal("the WRPRC outlives the WRPAC it is bound to")
		}
		raw, err := os.ReadFile(filepath.Join(f.options.publishDir, "reader-root.crl"))
		if err != nil {
			t.Fatal(err)
		}
		crl, err := x509.ParseRevocationList(raw)
		if err != nil || crl.NextUpdate.After(reader.NotAfter) {
			t.Fatalf("CRL next update %v, %v", crl, err)
		}
	})
}

func readTestRole(t *testing.T, dir, role string) issuedCertificate {
	t.Helper()
	c, err := readKeyAndChain(filepath.Join(dir, role+"-key.pem"), filepath.Join(dir, role+"-chain.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRegistrarRootIsProjected(t *testing.T) {
	caDir, publicDir := t.TempDir(), t.TempDir()
	if err := createDevelopmentRegistrarRoot(caDir, time.Now(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	if err := projectRegistrarRoot(caDir, publicDir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(publicDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "registrar-ca-cert.pem" {
		t.Fatalf("projected %v, %v: only the certificate, never the key", entries, err)
	}
}

func TestExportedSourceRoundTrip(t *testing.T) {
	f := newWalletRegistrationFixture(t)
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(f.source); err != nil {
		t.Fatal(err)
	}
	got, err := readWalletRegistrationSource("-", &buf)
	if err != nil || got.SourceID != f.source.SourceID || !slices.Equal(got.VCTs, f.source.VCTs) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := readWalletRegistrationSource("-", strings.NewReader(`{"source_id":"x","keys":"y"}`)); err == nil {
		t.Fatal("an export with unknown fields must be refused")
	}
	if _, err := readWalletRegistrationSource("-", strings.NewReader("")); err == nil {
		t.Fatal("an empty export (failed container run) must be refused")
	}
}

func TestProvisionWalletRegistrationRefuses(t *testing.T) {
	for name, mutate := range map[string]func(t *testing.T, f *walletRegistrationFixture){
		"no trust URL":          func(_ *testing.T, f *walletRegistrationFixture) { f.options.trustPublicURL = "" },
		"trust URL with a path": func(_ *testing.T, f *walletRegistrationFixture) { f.options.trustPublicURL = testTrustURL + "/trust" },
		"no organizationIdentifier type": func(_ *testing.T, f *walletRegistrationFixture) {
			f.options.organizationIdentifierType = ""
		},
		"organizationIdentifier type that is not three letters": func(_ *testing.T, f *walletRegistrationFixture) {
			f.options.organizationIdentifierType = "XY"
		},
		"no activated types": func(_ *testing.T, f *walletRegistrationFixture) { f.source.VCTs = nil },
		"an OIN other than the issuer certificate's": func(_ *testing.T, f *walletRegistrationFixture) {
			f.source.OIN = "99999999900000000300"
		},
		"a source without issuer certificate": func(_ *testing.T, f *walletRegistrationFixture) {
			f.source.CertificateSet = "nobody"
		},
		"no reader root": func(t *testing.T, f *walletRegistrationFixture) {
			removeCA(t, f.options.secretsDir, "reader")
		},
		"no registrar root": func(t *testing.T, f *walletRegistrationFixture) {
			removeCA(t, f.options.secretsDir, registrarRootRole)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newWalletRegistrationFixture(t)
			mutate(t, &f)
			err := provisionWalletRegistration(f.options, f.source)
			if err == nil {
				t.Fatal("expected refusal")
			}
			t.Log(err)
			if _, statErr := os.Stat(filepath.Join(f.options.secretsDir, "belastingdienst", "wrprc.jwt")); statErr == nil {
				t.Fatal("a refused run must not leave a registration certificate")
			}
		})
	}
}

func removeCA(t *testing.T, secrets, role string) {
	t.Helper()
	keyPath, certPath := caFiles(filepath.Join(secrets, "development-ca"), role)
	for _, p := range []string{keyPath, certPath} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRegistrarRootIsNeverReplaced(t *testing.T) {
	caDir := t.TempDir()
	now := time.Now()
	if err := createDevelopmentRegistrarRoot(caDir, now, rand.Reader); err != nil {
		t.Fatal(err)
	}
	_, certPath := caFiles(caDir, registrarRootRole)
	before, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := createDevelopmentRegistrarRoot(caDir, now, rand.Reader); err == nil {
		t.Fatal("an existing registrar root must not be replaced")
	}
	after, err := os.ReadFile(certPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("the registrar root changed")
	}
	block, _ := pem.Decode(after)
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !root.IsCA || root.KeyUsage&x509.KeyUsageCRLSign == 0 || root.CheckSignatureFrom(root) != nil {
		t.Fatalf("registrar root: %v", err)
	}
}

func TestWalletRegistrationSourceFromRelease(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	release := testReleaseWithTypes(t, now, []string{testIncome})
	t.Run("the activated types of the source", func(t *testing.T) {
		source, err := walletRegistrationSourceFromRelease(release, "belastingdienst")
		if err != nil {
			t.Fatal(err)
		}
		if source.OIN != "99999999900000000200" || source.Name != "Belastingdienst" || source.CertificateSet != "belastingdienst" ||
			!slices.Equal(source.VCTs, []string{testIncome}) {
			t.Fatalf("source %+v", source)
		}
	})
	t.Run("a source outside the active release", func(t *testing.T) {
		if _, err := walletRegistrationSourceFromRelease(release, "rvig"); err == nil || !strings.Contains(err.Error(), "not in the active Source Release") {
			t.Fatalf("got %v", err)
		}
	})
}

// testReleaseWithTypes is an active Source Release with one source,
// belastingdienst, that has the given types activated.
func testReleaseWithTypes(t *testing.T, now time.Time, vcts []string) onboarding.SourceRelease {
	t.Helper()
	secrets := t.TempDir()
	writeTestDevelopmentCAs(t, secrets, now)
	registration := sourceRegistration{
		SourceID: "belastingdienst", SourceOIN: "99999999900000000200", Name: "Belastingdienst",
		MetadataEndpoint: sourceMetadataEndpoint{Transport: sourceTransportUnsecured, Endpoint: "https://metadata.example"},
		DataAccess:       sourceDataAccess{Transport: sourceTransportUnsecured},
	}
	provider := newDevelopmentCAProvider(secrets, "https://issuer.example")
	provider.now = func() time.Time { return now }
	artifacts, err := provider.Provision(registration)
	if err != nil {
		t.Fatal(err)
	}
	certificateSet, err := publicCertificateSet(registration.certificateSetID(), artifacts)
	if err != nil {
		t.Fatal(err)
	}
	activation := &sourceActivation{
		SchemaVersion: registrySnapshotSchemaVersion, Source: registration, MetadataURL: "https://metadata.example",
		MetadataVersion: "1.0", MetadataPayloadDigest: strings.Repeat("a", 64), CheckedAt: now,
		ExpiresAt: now.Add(2 * time.Hour), FreshUntil: now.Add(15 * time.Minute), StaleUntil: now.Add(time.Hour),
	}
	var typeMetadata []onboarding.TypeMetadata
	for i, vct := range vcts {
		body := []byte(`{"display":[],"schema":{"type":"object","properties":{},"required":[]}}`)
		digest := sha256.Sum256(body)
		integrity := "sha256-" + base64.StdEncoding.EncodeToString(digest[:])
		offer := sourceOffer{ID: "offer" + string(rune('a'+i)), Label: "Offer", Parameters: map[string]any{}}
		definition := sourceAttestationDefinition{
			TypeID: "type" + string(rune('a'+i)), TypeVersion: "1.0", Offers: []sourceOffer{offer},
			GraphQL:        sourceGraphQL{Endpoint: "/graphql", Document: "query Income($bsn: String!) { income(bsn: $bsn) { value } }", SubjectVariable: "bsn", ResultPointer: "/data/income"},
			MappingProfile: "gbo-simple-v1", Display: testDisplay("Example"),
			Claims: testClaims(map[string]mappingRule{"value": {Pointer: "/value", Datatype: "string"}}),
		}
		activation.Types = append(activation.Types, activatedType{TypeID: definition.TypeID, TypeVersion: "1.0", VCT: vct,
			VCTIntegrity: integrity, Offers: []sourceOffer{offer}, Definition: definition})
		typeMetadata = append(typeMetadata, onboarding.TypeMetadata{VCT: vct, Version: "1.0", Integrity: integrity, MediaType: "application/json", Bytes: body})
	}
	candidate, err := registryCandidateFromActivation(activation, certificateSet, typeMetadata)
	if err != nil {
		t.Fatal(err)
	}
	release, err := onboarding.NewSourceRelease(now, []onboarding.SourceCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func organizationIdentifier(c *x509.Certificate) string {
	for _, n := range c.Subject.Names {
		if n.Type.Equal(organizationIdentifierOID) {
			s, _ := n.Value.(string)
			return s
		}
	}
	return ""
}

// assertRegistrationCertificate checks the WRPRC against what the NL Wallet
// v0.6 requires of an attestation provider's registration.
func assertRegistrationCertificate(t *testing.T, f walletRegistrationFixture, registrar *x509.Certificate) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.options.secretsDir, "belastingdienst", "wrprc.jwt"))
	if err != nil {
		t.Fatal(err)
	}
	header, _, claims := verifiedJWT(t, string(raw), registrar)
	if header.ExtraHeaders["typ"] != "rc-wrp+jwt" || header.ExtraHeaders["iat"] == nil {
		t.Fatalf("header %v", header.ExtraHeaders)
	}
	if claims["sub"] != organizationIdentifier(f.wrpac(t).chain[0]) {
		t.Fatalf("sub %v is not the WRPAC organizationIdentifier", claims["sub"])
	}
	provides := claims["provides_attestations"].([]any)[0].(map[string]any)["meta"].(map[string]any)["vct_values"].([]any)
	if len(provides) != 1 || provides[0] != testIncome {
		t.Fatalf("provides_attestations %v", provides)
	}
	if claims["entitlements"].([]any)[0] != wrprcPubEAAEntitlement || claims["public_body"] != true {
		t.Fatalf("entitlements %v, public_body %v", claims["entitlements"], claims["public_body"])
	}
	status := claims["status"].(map[string]any)
	if status["idx"] != "0" || status["uri"] != testTrustURL+"/wrprc/1" {
		t.Fatalf("status %v", status)
	}
	iat, exp := time.Unix(int64(claims["iat"].(float64)), 0), time.Unix(int64(claims["exp"].(float64)), 0)
	if exp.After(iat.AddDate(0, 12, 0)) {
		t.Fatal("the NL Wallet refuses a WRPRC valid for more than twelve months")
	}
	// A legal person: sub_ln, no natural-person names.
	if claims["id"] != "gbo-belastingdienst-issuer" || claims["sub_ln"] != "Belastingdienst" || claims["country"] != "NL" ||
		claims["sub_gn"] != nil || claims["sub_fn"] != nil || claims["supervisory_authority"] == nil {
		t.Fatalf("subject fields %v", claims)
	}
	description := claims["srv_description"].([]any)[0].([]any)
	for _, d := range description {
		if lang := d.(map[string]any)["lang"]; lang != "nl" && lang != "en" {
			t.Fatalf("language tag %v", lang)
		}
	}
	if !slices.Contains(claims["policy_id"].([]any), any(wrprcPolicyIdentifier)) {
		t.Fatalf("policy_id %v", claims["policy_id"])
	}
}
