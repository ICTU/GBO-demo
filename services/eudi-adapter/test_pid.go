package main

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The test PID: a PID of the NL type that the own credential issuer gives to
// wallets without a real one (the EUDI reference wallet), so the
// disclosure-based route can be shown with them. A test fixture, not a PID
// Provider: its credentials chain to a development root of its own, which
// only the demo's wallet builds and verifier trust. Its data come from the
// test-pid-source stub, outside the Source Release.
const (
	testPIDID           = "test-pid"
	testPIDRootRole     = "test-pid"
	testPIDName         = "GBO test-PID"
	testPIDType         = nlPIDType
	testPIDKvKNumber    = "99990100"
	wrprcPIDEntitlement = "https://uri.etsi.org/19475/Entitlement/PID_Provider"
	// testPIDRootFile is the public test PID root next to its registration.
	testPIDRootFile = "root-cert.pem"
)

var testPIDSubject = pkix.Name{CommonName: testPIDName, Organization: []string{"GBO Demo"}, Country: []string{"NL"}}

// testPIDDirs are where the test PID's public root certificate goes and
// where its root key lives: outside the secrets directory, which containers
// mount, so no container holds a key that signs PIDs the verifier trusts.
type testPIDDirs struct{ publicDir, rootDir string }

func parseTestPIDOptions(command string, arguments []string, stderr io.Writer) (walletRegistrationOptions, testPIDDirs, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(stderr)
	options := walletRegistrationOptions{now: time.Now, random: rand.Reader, kvkNumber: testPIDKvKNumber}
	var dirs testPIDDirs
	set.StringVar(&options.secretsDir, "secrets-dir", getEnv("ONBOARDING_SECRETS_DIR", ".local/secrets"), "filesystem secret directory")
	set.StringVar(&options.publishDir, "publish-dir", getEnv("EUDI_TRUST_PUBLISH_DIR", ".local/public-trust"), "directory the trust host serves")
	set.StringVar(&options.trustPublicURL, "trust-public-url", os.Getenv("EUDI_TRUST_PUBLIC_URL"), "public root URL of the trust host")
	set.StringVar(&dirs.publicDir, "public-certificates-dir", getEnv("ONBOARDING_PUBLIC_CERTIFICATES_DIR", ".local/public-certificates"), "public-only certificate projection directory")
	set.StringVar(&dirs.rootDir, "root-dir", getEnv("TEST_PID_ROOT_DIR", ".local/test-pid-root"), "directory of the test PID root key, outside every container mount")
	if err := set.Parse(arguments); err != nil {
		return options, dirs, err
	}
	if set.NArg() != 0 {
		return options, dirs, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	return options, dirs, nil
}

// provisionTestPID creates the test PID root once, issues the issuer and
// status certificates under it, and registers the test PID provider like a
// source: a WRPAC under the reader root and a WRPRC with the PID_Provider
// entitlement. A second run keeps the root and every key. Everything the
// registration needs is checked before anything is created, so a refused
// run leaves no half-provisioned test PID behind.
func provisionTestPID(options walletRegistrationOptions, dirs testPIDDirs) error {
	caDir := filepath.Join(options.secretsDir, "development-ca")
	for role, description := range map[string]string{"reader": "pre-provisioned development reader CA",
		registrarRootRole: "development registrar root (make create-development-registrar-root)"} {
		if _, err := loadRole(caDir, role, description); err != nil {
			return err
		}
	}
	if _, err := parseDevelopmentRootURL(options.trustPublicURL, false); err != nil {
		return fmt.Errorf("trust public URL: %w", err)
	}
	state, err := readWalletRegistrationState(caDir)
	if err != nil {
		return err
	}
	if _, err := state.kvkNumber(testPIDID, options.kvkNumber); err != nil {
		return err
	}
	keyPath, certPath := caFiles(dirs.rootDir, testPIDRootRole)
	if !fileExists(keyPath) && !fileExists(certPath) {
		if err := createDevelopmentRoot(dirs.rootDir, testPIDRootRole,
			pkix.Name{CommonName: "GBO test-PID root", Organization: []string{"GBO Demo"}, Country: []string{"NL"}},
			options.now(), options.random); err != nil {
			return err
		}
	}
	root, err := loadRole(dirs.rootDir, testPIDRootRole, "test PID root")
	if err != nil {
		return err
	}
	if err := projectRoot(dirs.rootDir, testPIDRootRole, filepath.Join(dirs.publicDir, "development-ca")); err != nil {
		return err
	}
	dir := filepath.Join(options.secretsDir, testPIDID)
	// The materializer reads the public root here: the issuer trusts PIDs
	// under it.
	if err := projectRootAs(dirs.rootDir, testPIDRootRole, dir, testPIDRootFile); err != nil {
		return err
	}
	now := options.now()
	if _, err := ensureCertificate(options, dir, "issuer", root, testPIDLeafTemplate(issuerEKUOID, now)); err != nil {
		return err
	}
	if _, err := ensureCertificate(options, dir, "status", root, testPIDLeafTemplate(statusEKUOID, now)); err != nil {
		return err
	}
	return registerWallet(options, walletRegistrationSource{
		SourceID: testPIDID, Name: testPIDName, CertificateSet: testPIDID, VCTs: []string{testPIDType},
	}, wrprcPIDEntitlement, false)
}

// testPIDLeafTemplate is the issuer or the status certificate; both carry
// the same subject, as the status list signer must.
func testPIDLeafTemplate(eku asn1.ObjectIdentifier, now time.Time) *x509.Certificate {
	return &x509.Certificate{
		Subject:   testPIDSubject,
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		UnknownExtKeyUsage: []asn1.ObjectIdentifier{eku},
	}
}

// testPIDClaims describes the claims of the NL PID type as the stub fills
// them, with the labels of the NL Wallet's type metadata. The address is
// optional: a person living abroad has none.
var testPIDClaims = []issuerClaim{
	testPIDClaim([]string{"bsn"}, true, "BSN", "BSN"),
	testPIDClaim([]string{"given_name"}, true, "Voornaam", "Given name"),
	testPIDClaim([]string{"family_name"}, true, "Achternaam", "Family name"),
	testPIDClaim([]string{"birthdate"}, true, "Geboortedatum", "Date of birth"),
	testPIDClaim([]string{"age_over_18"}, true, "18+", "Over 18"),
	testPIDClaim([]string{"nationalities"}, true, "Nationaliteiten", "Nationalities"),
	testPIDClaim([]string{"address"}, false, "Adres", "Address"),
	testPIDClaim([]string{"address", "street_address"}, false, "Straatnaam", "Street"),
	testPIDClaim([]string{"address", "house_number"}, false, "Huisnummer", "House number"),
	testPIDClaim([]string{"address", "postal_code"}, false, "Postcode", "Postal code"),
	testPIDClaim([]string{"address", "locality"}, false, "Stad", "City"),
	testPIDClaim([]string{"address", "country"}, false, "Land", "Country"),
}

func testPIDClaim(path []string, mandatory bool, nl, en string) issuerClaim {
	return issuerClaim{Path: path, Mandatory: mandatory,
		Display: []issuerDisplay{{Name: nl, Locale: "nl"}, {Name: en, Locale: "en"}}}
}

// testPIDProvider is the test PID provider when make provision-test-pid has
// run: its own issuer and status certificates and its wallet registration,
// one credential configuration fed by the test-pid-source stub. Without the
// material there is no test PID provider.
func testPIDProvider(options credentialIssuerOptions, seen derivedNames) (issuerProviderFile, bool, error) {
	dir := filepath.Join(options.secretsDir, testPIDID)
	if !fileExists(dir) {
		return issuerProviderFile{}, false, nil
	}
	if earlier, ok := seen.vcts[testPIDType]; ok {
		return issuerProviderFile{}, false, fmt.Errorf("vct %s is activated for %s as well as for the test PID", testPIDType, earlier)
	}
	if options.testPIDSourceURL == "" {
		return issuerProviderFile{}, false, errors.New("the test PID is provisioned, but its bronpoort URL is not set (TEST_PID_SOURCE_URL)")
	}
	provider := issuerProviderFile{Name: testPIDID, TestPersonsFile: options.testPIDPersonsFile}
	if provider.TestPersonsFile == "" {
		provider.TestPersonsFile = "/test-pid-persons/personen.json"
	}
	if err := copyWalletRegistration(options, testPIDID, testPIDID, []string{testPIDType}, &provider, "make provision-test-pid"); err != nil {
		return provider, false, err
	}
	var err error
	if provider.Issuer, err = copyTestPIDLeaf(options, dir, "issuer"); err != nil {
		return provider, false, err
	}
	if provider.Status, err = copyTestPIDLeaf(options, dir, "status"); err != nil {
		return provider, false, err
	}
	provider.CredentialConfigurations = []issuerConfigurationFile{{
		ID: testPIDID + "-pid", VCT: testPIDType, Scope: testPIDID + "-pid",
		Display:   []issuerDisplay{{Name: "GBO test-PID", Locale: "nl"}, {Name: "GBO test PID", Locale: "en"}},
		Claims:    testPIDClaims,
		ValidDays: credentialValidDays, StatusList: "pid", SourceURL: options.testPIDSourceURL,
	}}
	return provider, true, nil
}

// copyTestPIDLeaf copies a provisioned leaf key and certificate. An expired
// certificate is refused: the issuer would sign with it.
func copyTestPIDLeaf(options credentialIssuerOptions, dir, role string) (issuerKeyFiles, error) {
	leaf, err := readKeyAndChain(filepath.Join(dir, role+"-key.pem"), filepath.Join(dir, role+"-chain.pem"))
	if err != nil {
		return issuerKeyFiles{}, fmt.Errorf("%s certificate: %w; run make provision-test-pid", role, err)
	}
	if !options.now().Before(leaf.chain[0].NotAfter) {
		return issuerKeyFiles{}, fmt.Errorf("the %s certificate has expired; run make provision-test-pid again", role)
	}
	out := filepath.Join(options.outputDir, testPIDID)
	if err := ensurePrivateDirectory(out); err != nil {
		return issuerKeyFiles{}, err
	}
	keyFile, chainFile := role+"-key.pem", role+"-chain.pem"
	if err := writeKeyAndCertificate(out, filepath.Join(out, keyFile), filepath.Join(out, chainFile), leaf.key, [][]byte{leaf.chain[0].Raw}); err != nil {
		return issuerKeyFiles{}, err
	}
	return issuerKeyFiles{KeyFile: testPIDID + "/" + keyFile, ChainFile: testPIDID + "/" + chainFile}, nil
}
