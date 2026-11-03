package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"

	"gbo-demo/eudi-adapter/internal/onboarding"
	"gbo-demo/eudi-adapter/internal/postgresregistry"
	"gbo-demo/oid4vc-kit/statuslist"
)

// Wallet registration material for the own credential issuer: per source a
// Wallet-Relying Party Access Certificate (WRPAC) under the development
// reader root and a Registration Certificate (WRPRC) signed by a separate
// development registrar root, plus the CRL and WRPRC status list a wallet
// fetches from the trust host.
//
// The registrar is its own root because a wallet recognises a WRPRC signer
// only by the trust list it chains to, not by an EKU: under the reader root,
// every access certificate holder could register itself.

const (
	registrarRootRole       = "registrar"
	wrpacCRLName            = "reader-root.crl"
	wrprcStatusListPath     = "wrprc/1"
	walletRegistrationState = "wallet-registration.json"
	wrprcStatusListSize     = 1024
	wrprcPolicyIdentifier   = "0.4.0.19475.3.1"
	wrprcPubEAAEntitlement  = "https://uri.etsi.org/19475/Entitlement/PUB_EAA_Provider"
	wrprcJWTType            = "rc-wrp+jwt"
	walletRegistrationValid = 365 * 24 * time.Hour
	// requestEncryptionKeyFile is the per-source key of encrypted credential
	// requests, next to the WRPAC.
	requestEncryptionKeyFile = "request-encryption-key.pem"
)

var (
	organizationIdentifierOID = asn1.ObjectIdentifier{2, 5, 4, 97}
	kvkNumberPattern          = regexp.MustCompile(`^[0-9]{8}$`)
	registrarSubject          = pkix.Name{CommonName: "GBO development registrar", Organization: []string{"GBO Demo"}, Country: []string{"NL"}}
)

type walletRegistrationOptions struct {
	secretsDir, publishDir, trustPublicURL string
	// kvkNumber is the source's number in the Dutch trade register. It
	// becomes the organizationIdentifier NTRNL-<number> (ETSI EN 319 412-1
	// section 5.1.4): no identity type there fits an OIN.
	kvkNumber string
	now       func() time.Time
	random    io.Reader
}

// walletRegistrationSource is what the active Source Release says about one
// source. A container in the eudi network exports it from the Source
// Registry; the signing runs on the host, so no container sees a CA key.
type walletRegistrationSource struct {
	SourceID       string   `json:"source_id"`
	OIN            string   `json:"oin"`
	Name           string   `json:"name"`
	CertificateSet string   `json:"certificate_set"`
	VCTs           []string `json:"vcts"`
}

func runWalletRegistrationCommand(ctx context.Context, arguments []string, stdout, stderr io.Writer) (bool, error) {
	if len(arguments) == 0 {
		return false, nil
	}
	switch arguments[0] {
	case "create-development-registrar-root":
		set := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
		set.SetOutput(stderr)
		secretsDir := set.String("secrets-dir", getEnv("ONBOARDING_SECRETS_DIR", ".local/secrets"), "filesystem secret directory")
		publicDir := set.String("public-certificates-dir", getEnv("ONBOARDING_PUBLIC_CERTIFICATES_DIR", ".local/public-certificates"), "public-only certificate projection directory")
		if err := set.Parse(arguments[1:]); err != nil {
			return true, err
		}
		caDir := filepath.Join(*secretsDir, "development-ca")
		if err := createDevelopmentRegistrarRoot(caDir, time.Now(), rand.Reader); err != nil {
			return true, err
		}
		if err := projectRegistrarRoot(caDir, filepath.Join(*publicDir, "development-ca")); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(stdout, "development registrar root created; this command is for local development only")
		return true, nil
	case "export-wallet-registration-source":
		set := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
		set.SetOutput(stderr)
		sourceID := set.String("source-id", "", "source identifier in the active Source Release")
		databaseURL := set.String("database-url", os.Getenv("SOURCE_REGISTRY_DATABASE_URL"), "read-only PostgreSQL Source Registry connection URL")
		databaseSchema := set.String("database-schema", getEnv("SOURCE_REGISTRY_SCHEMA", "source_registry"), "PostgreSQL Source Registry schema")
		if err := set.Parse(arguments[1:]); err != nil {
			return true, err
		}
		source, err := loadWalletRegistrationSource(ctx, *databaseURL, *databaseSchema, *sourceID)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(stdout).Encode(source)
	case "provision-wallet-registration":
		options, sourceFile, err := parseWalletRegistrationOptions(arguments[0], arguments[1:], stderr)
		if err != nil {
			return true, err
		}
		source, err := readWalletRegistrationSource(sourceFile, os.Stdin)
		if err != nil {
			return true, err
		}
		if err := provisionWalletRegistration(options, source); err != nil {
			return true, fmt.Errorf("provision wallet registration: %w", err)
		}
		_, _ = fmt.Fprintf(stdout, "wallet registration provisioned for source %s; this command is for local development only\n", source.SourceID)
		return true, nil
	case "provision-test-pid":
		options, dirs, err := parseTestPIDOptions(arguments[0], arguments[1:], stderr)
		if err != nil {
			return true, err
		}
		if err := provisionTestPID(options, dirs); err != nil {
			return true, fmt.Errorf("provision test PID: %w", err)
		}
		_, _ = fmt.Fprintln(stdout, "test PID provider provisioned; this command is for local development only")
		return true, nil
	}
	return false, nil
}

func parseWalletRegistrationOptions(command string, arguments []string, stderr io.Writer) (walletRegistrationOptions, string, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(stderr)
	options := walletRegistrationOptions{now: time.Now, random: rand.Reader}
	sourceFile := set.String("source-file", "-", "source exported by export-wallet-registration-source; - reads stdin")
	set.StringVar(&options.secretsDir, "secrets-dir", getEnv("ONBOARDING_SECRETS_DIR", ".local/secrets"), "filesystem secret directory")
	set.StringVar(&options.publishDir, "publish-dir", getEnv("EUDI_TRUST_PUBLISH_DIR", ".local/public-trust"), "directory the trust host serves")
	set.StringVar(&options.trustPublicURL, "trust-public-url", os.Getenv("EUDI_TRUST_PUBLIC_URL"), "public root URL of the trust host")
	set.StringVar(&options.kvkNumber, "kvk-number", "", "the source's KvK number; required the first time, recorded afterwards")
	if err := set.Parse(arguments); err != nil {
		return options, "", err
	}
	if set.NArg() != 0 {
		return options, "", fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	return options, *sourceFile, nil
}

func readWalletRegistrationSource(path string, stdin io.Reader) (walletRegistrationSource, error) {
	in := stdin
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return walletRegistrationSource{}, err
		}
		defer func() { _ = f.Close() }()
		in = f
	}
	var source walletRegistrationSource
	decoder := json.NewDecoder(in)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return walletRegistrationSource{}, fmt.Errorf("read the exported source: %w", err)
	}
	return source, nil
}

// loadWalletRegistrationSource reads the source and its activated types
// from the active Source Release.
func loadWalletRegistrationSource(ctx context.Context, databaseURL, databaseSchema, sourceID string) (walletRegistrationSource, error) {
	if !sourceIDPattern.MatchString(sourceID) {
		return walletRegistrationSource{}, errors.New("--source-id is invalid")
	}
	store, err := postgresregistry.Open(ctx, postgresregistry.Options{DatabaseURL: databaseURL, Schema: databaseSchema})
	if err != nil {
		return walletRegistrationSource{}, err
	}
	defer store.Close()
	release, err := store.ActiveRelease(ctx)
	if err != nil {
		return walletRegistrationSource{}, err
	}
	return walletRegistrationSourceFromRelease(release, sourceID)
}

func walletRegistrationSourceFromRelease(release onboarding.SourceRelease, sourceID string) (walletRegistrationSource, error) {
	for _, source := range release.Sources {
		if source.SourceID != sourceID {
			continue
		}
		activation, err := activationFromRegistryCandidate(onboarding.SourceCandidate{
			SourceID: source.SourceID, Snapshot: source.Snapshot,
			CertificateSet: source.CertificateSet, TypeMetadata: source.TypeMetadata,
		})
		if err != nil {
			return walletRegistrationSource{}, fmt.Errorf("source %s activation: %w", sourceID, err)
		}
		out := walletRegistrationSource{
			SourceID: sourceID, OIN: activation.Source.SourceOIN, Name: activation.Source.Name,
			CertificateSet: source.CertificateSet.ID,
		}
		for _, t := range activation.Types {
			out.VCTs = append(out.VCTs, t.VCT)
		}
		if len(out.VCTs) == 0 {
			return walletRegistrationSource{}, fmt.Errorf("source %s has no activated types in the active Source Release", sourceID)
		}
		return out, nil
	}
	return walletRegistrationSource{}, fmt.Errorf("source %s is not in the active Source Release", sourceID)
}

// createDevelopmentRegistrarRoot creates the registrar trust anchor once. It
// never replaces one: wallets trust it by its key.
func createDevelopmentRegistrarRoot(caDir string, now time.Time, random io.Reader) error {
	return createDevelopmentRoot(caDir, registrarRootRole,
		pkix.Name{CommonName: "GBO development registrar root", Organization: []string{"GBO Demo"}, Country: []string{"NL"}}, now, random)
}

// createDevelopmentRoot creates a development trust anchor for role once
// and never replaces it.
func createDevelopmentRoot(caDir, role string, subject pkix.Name, now time.Time, random io.Reader) error {
	keyPath, certPath := caFiles(caDir, role)
	for _, p := range []string{keyPath, certPath} {
		if fileExists(p) {
			return fmt.Errorf("development %s root file %q already exists; the root is never replaced", role, p)
		}
	}
	if err := ensurePrivateDirectory(caDir); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), random)
	if err != nil {
		return err
	}
	serial, err := randomSerial(random)
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      subject,
		NotBefore:    now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true, IsCA: true, MaxPathLen: 0, MaxPathLenZero: true,
	}
	der, err := x509.CreateCertificate(random, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	return writeKeyAndCertificate(caDir, keyPath, certPath, key, [][]byte{der})
}

// projectRegistrarRoot copies the registrar root certificate next to the
// public CA certificates, for the wallets' trust lists.
func projectRegistrarRoot(caDir, publicDir string) error {
	return projectRoot(caDir, registrarRootRole, publicDir)
}

func projectRoot(caDir, role, publicDir string) error {
	_, certPath := caFiles(caDir, role)
	body, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		return err
	}
	return writeFileAtomically(publicDir, filepath.Base(certPath), body, 0o644)
}

func provisionWalletRegistration(options walletRegistrationOptions, source walletRegistrationSource) error {
	if !sourceIDPattern.MatchString(source.SourceID) || !sourceIDPattern.MatchString(source.CertificateSet) {
		return errors.New("the source needs a valid source id and certificate set")
	}
	if source.SourceID == testPIDID || source.CertificateSet == testPIDID {
		return fmt.Errorf("%q is reserved for the test PID provider", testPIDID)
	}
	if !sourceOINPattern.MatchString(source.OIN) || source.Name == "" || len(source.VCTs) == 0 {
		return errors.New("the source needs an OIN, a name and activated types")
	}
	if err := checkSourceIdentity(filepath.Join(options.secretsDir, source.CertificateSet), source); err != nil {
		return err
	}
	return registerWallet(options, source, wrprcPubEAAEntitlement)
}

// registerWallet issues the WRPAC and WRPRC of a party that provides
// attestations with the given entitlement, and publishes the trust host's
// files.
func registerWallet(options walletRegistrationOptions, source walletRegistrationSource, entitlement string) error {
	trustURL, err := parseDevelopmentRootURL(options.trustPublicURL, false)
	if err != nil {
		return fmt.Errorf("trust public URL: %w", err)
	}
	trustBase := strings.TrimSuffix(trustURL.String(), "/")
	now := options.now().UTC()
	caDir := filepath.Join(options.secretsDir, "development-ca")
	reader, err := loadRole(caDir, "reader", "pre-provisioned development reader CA")
	if err != nil {
		return err
	}
	registrar, err := loadRole(caDir, registrarRootRole, "development registrar root (make create-development-registrar-root)")
	if err != nil {
		return err
	}
	sourceDir := filepath.Join(options.secretsDir, source.CertificateSet)

	state, err := readWalletRegistrationState(caDir)
	if err != nil {
		return err
	}
	// Every WRPAC and WRPRC carries the trust URL, so a new one has to reach
	// all sources together.
	if state.TrustPublicURL != "" && state.TrustPublicURL != trustBase {
		return fmt.Errorf("the trust URL was %s; to move to %s, remove %s and provision every source again",
			state.TrustPublicURL, trustBase, filepath.Join(caDir, walletRegistrationState))
	}
	state.TrustPublicURL = trustBase
	kvk, err := state.kvkNumber(source.SourceID, options.kvkNumber)
	if err != nil {
		return err
	}

	orgID := "NTRNL-" + kvk
	wrpac, err := ensureCertificate(options, sourceDir, "wrpac", reader, wrpacTemplate(source.Name, orgID, trustBase+"/"+wrpacCRLName, now))
	if err != nil {
		return err
	}
	if err := ensureRequestEncryptionKey(options, sourceDir); err != nil {
		return err
	}
	signer, err := ensureCertificate(options, caDir, "wrprc-signer", registrar, registrarLeafTemplate(false, now))
	if err != nil {
		return err
	}
	status, err := ensureCertificate(options, caDir, "wrprc-status", registrar, registrarLeafTemplate(true, now))
	if err != nil {
		return err
	}
	idx, err := state.statusIndex(source.SourceID)
	if err != nil {
		return err
	}
	if err := writeWalletRegistrationState(caDir, state); err != nil {
		return err
	}
	// The registration lasts as long as the certificates it rests on.
	expires := earliest(now.AddDate(1, 0, -1), wrpac.chain[0].NotAfter, signer.chain[0].NotAfter)
	rc, err := signRegistrationCertificate(signer, registrationPayload(source, entitlement, orgID, trustBase, idx, now, expires), now)
	if err != nil {
		return err
	}
	if err := writeFileAtomically(sourceDir, "wrprc.jwt", []byte(rc), 0o644); err != nil {
		return err
	}
	return publishTrust(options, reader, status, trustBase, now)
}

// issuedCertificate is a key with its certificate, leaf first, without the
// root.
type issuedCertificate struct {
	key   *ecdsa.PrivateKey
	chain []*x509.Certificate
}

func loadRole(caDir, role, description string) (issuedCertificate, error) {
	keyPath, certPath := caFiles(caDir, role)
	if !fileExists(keyPath) || !fileExists(certPath) {
		return issuedCertificate{}, fmt.Errorf("%s is required at %q and %q", description, keyPath, certPath)
	}
	key, cert, err := loadDevelopmentCA(keyPath, certPath)
	if err != nil {
		return issuedCertificate{}, fmt.Errorf("load %s: %w", description, err)
	}
	return issuedCertificate{key: key, chain: []*x509.Certificate{cert}}, nil
}

func caFiles(caDir, role string) (string, string) {
	return filepath.Join(caDir, role+"-ca-key.pem"), filepath.Join(caDir, role+"-ca-cert.pem")
}

// checkSourceIdentity refuses a source whose existing issuer certificate
// names another OIN: the WRPAC and the credentials must be the same party.
func checkSourceIdentity(sourceDir string, source walletRegistrationSource) error {
	issuer, err := loadDevelopmentLeafCertificate(filepath.Join(sourceDir, "issuer-cert.der.b64"))
	if err != nil {
		return fmt.Errorf("load the issuer certificate of source %s (make provision-development-certificates first): %w", source.SourceID, err)
	}
	oin, _, err := certificateSourceIdentity(issuer)
	if err != nil {
		return err
	}
	if oin != source.OIN {
		return fmt.Errorf("the issuer certificate of source %s names OIN %s, the active release %s", source.SourceID, oin, source.OIN)
	}
	return nil
}

// wrpacTemplate is a legal person's access certificate as the NL Wallet
// reads it: country, organization name and organizationIdentifier, and a
// CRL distribution point on every certificate of the chain.
func wrpacTemplate(name, orgID, crl string, now time.Time) *x509.Certificate {
	return &x509.Certificate{
		Subject: pkix.Name{CommonName: name, Organization: []string{name}, Country: []string{"NL"},
			ExtraNames: []pkix.AttributeTypeAndValue{{Type: organizationIdentifierOID, Value: orgID}}},
		NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(walletRegistrationValid),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		CRLDistributionPoints: []string{crl},
	}
}

// registrarLeafTemplate is the WRPRC signer, or the status list signer
// with the status signing EKU. Both carry the registrar's subject: the
// status list signer must have the subject of the certificate that signed
// what it covers.
func registrarLeafTemplate(statusSigning bool, now time.Time) *x509.Certificate {
	t := &x509.Certificate{Subject: registrarSubject, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.Add(walletRegistrationValid),
		KeyUsage: x509.KeyUsageDigitalSignature}
	if statusSigning {
		t.UnknownExtKeyUsage = []asn1.ObjectIdentifier{statusEKUOID}
	}
	return t
}

// ensureCertificate keeps an existing key and certificate when the
// certificate still matches template, is issued by parent and is valid for
// another month; otherwise it issues a new certificate for the same key.
func ensureCertificate(options walletRegistrationOptions, dir, role string, parent issuedCertificate, template *x509.Certificate) (issuedCertificate, error) {
	if err := ensurePrivateDirectory(dir); err != nil {
		return issuedCertificate{}, err
	}
	parentCert := parent.chain[0]
	if !options.now().AddDate(0, 1, 0).Before(parentCert.NotAfter) {
		return issuedCertificate{}, fmt.Errorf("%s cannot be issued: %s expires on %s; renew it first",
			role, parentCert.Subject, parentCert.NotAfter.Format(time.DateOnly))
	}
	keyPath, certPath := filepath.Join(dir, role+"-key.pem"), filepath.Join(dir, role+"-chain.pem")
	var key *ecdsa.PrivateKey
	// A key without its certificate (an interrupted run) is replaced: no
	// certificate was ever published for it.
	if fileExists(keyPath) && fileExists(certPath) {
		existing, err := readKeyAndChain(keyPath, certPath)
		if err != nil {
			return issuedCertificate{}, fmt.Errorf("load %s: %w", role, err)
		}
		if certificateMatches(existing.chain[0], parent.chain[0], template, options.now()) {
			return existing, nil
		}
		key = existing.key
	} else {
		var err error
		if key, err = ecdsa.GenerateKey(elliptic.P256(), options.random); err != nil {
			return issuedCertificate{}, err
		}
	}
	serial, err := randomSerial(options.random)
	if err != nil {
		return issuedCertificate{}, err
	}
	template.SerialNumber = serial
	// A certificate cannot outlive the one that issued it.
	if template.NotAfter.After(parent.chain[0].NotAfter) {
		template.NotAfter = parent.chain[0].NotAfter
	}
	der, err := x509.CreateCertificate(options.random, template, parent.chain[0], &key.PublicKey, parent.key)
	if err != nil {
		return issuedCertificate{}, fmt.Errorf("issue %s: %w", role, err)
	}
	if err := writeKeyAndCertificate(dir, keyPath, certPath, key, [][]byte{der}); err != nil {
		return issuedCertificate{}, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return issuedCertificate{}, err
	}
	return issuedCertificate{key: key, chain: []*x509.Certificate{cert}}, nil
}

func certificateMatches(cert, parent, template *x509.Certificate, now time.Time) bool {
	// Compared as DER: a parsed certificate keeps organizationIdentifier in
	// Subject.Names, which Subject.String does not print.
	subject, err := asn1.Marshal(template.Subject.ToRDNSequence())
	return err == nil && cert.CheckSignatureFrom(parent) == nil &&
		bytes.Equal(cert.RawSubject, subject) &&
		slices.Equal(cert.CRLDistributionPoints, template.CRLDistributionPoints) &&
		slices.EqualFunc(cert.UnknownExtKeyUsage, template.UnknownExtKeyUsage, asn1.ObjectIdentifier.Equal) &&
		now.AddDate(0, 1, 0).Before(cert.NotAfter)
}

// walletRegistrationRecord is what earlier runs decided: the trust URL in
// the certificates and each source's index in the WRPRC status list.
type walletRegistrationRecord struct {
	TrustPublicURL string            `json:"trust_public_url"`
	StatusIndices  map[string]int    `json:"status_indices"`
	KvKNumbers     map[string]string `json:"kvk_numbers"`
}

func readWalletRegistrationState(caDir string) (walletRegistrationRecord, error) {
	state := walletRegistrationRecord{StatusIndices: map[string]int{}, KvKNumbers: map[string]string{}}
	raw, err := os.ReadFile(filepath.Join(caDir, walletRegistrationState))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("read %s: %w", walletRegistrationState, err)
	}
	if state.StatusIndices == nil {
		state.StatusIndices = map[string]int{}
	}
	if state.KvKNumbers == nil {
		state.KvKNumbers = map[string]string{}
	}
	return state, nil
}

// kvkNumber returns the source's KvK number: the one given, recorded the
// first time, or the recorded one. A different number for a registered
// source is refused: it would make the source another organisation.
func (s *walletRegistrationRecord) kvkNumber(sourceID, given string) (string, error) {
	recorded := s.KvKNumbers[sourceID]
	switch {
	case given == "" && recorded == "":
		return "", fmt.Errorf("source %s has no KvK number yet; pass it (KVK_NUMBER=<8 digits>)", sourceID)
	case given == "":
		return recorded, nil
	case !kvkNumberPattern.MatchString(given):
		return "", errors.New("a KvK number has eight digits")
	case recorded != "" && recorded != given:
		return "", fmt.Errorf("source %s is registered with KvK number %s; remove it from %s to register it as %s",
			sourceID, recorded, walletRegistrationState, given)
	}
	// One number is one organisation: two parties with the same number would
	// share an organizationIdentifier.
	for other, number := range s.KvKNumbers {
		if number == given && other != sourceID {
			return "", fmt.Errorf("KvK number %s is registered for %s", given, other)
		}
	}
	s.KvKNumbers[sourceID] = given
	return given, nil
}

func writeWalletRegistrationState(caDir string, state walletRegistrationRecord) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomically(caDir, walletRegistrationState, raw, 0o600)
}

// statusIndex returns the source's index in the WRPRC status list,
// assigning one past the highest the first time, so an index is never
// handed out twice.
func (s *walletRegistrationRecord) statusIndex(sourceID string) (int, error) {
	if idx, ok := s.StatusIndices[sourceID]; ok {
		return idx, nil
	}
	next := 0
	for _, idx := range s.StatusIndices {
		next = max(next, idx+1)
	}
	if next >= wrprcStatusListSize {
		return 0, errors.New("the WRPRC status list is full")
	}
	s.StatusIndices[sourceID] = next
	return next, nil
}

func earliest(first time.Time, others ...time.Time) time.Time {
	out := first
	for _, t := range others {
		if t.Before(out) {
			out = t
		}
	}
	return out
}

// registrationPayload is a WRPRC (ETSI TS 119 475) for a public body that
// provides attestations, with the fields the NL Wallet v0.6 requires.
func registrationPayload(source walletRegistrationSource, entitlement, orgID, trustBase string, idx int, now, expires time.Time) map[string]any {
	provides := make([]map[string]any, 0, len(source.VCTs))
	for _, vct := range source.VCTs {
		provides = append(provides, map[string]any{"format": "dc+sd-jwt", "meta": map[string]any{"vct_values": []string{vct}}})
	}
	description := []map[string]string{
		{"lang": "nl", "value": "Uitgifte van attestaties door " + source.Name},
		{"lang": "en", "value": "Issuance of attestations by " + source.Name},
	}
	return map[string]any{
		"id": "gbo-" + source.SourceID + "-issuer", "name": source.Name,
		"sub": orgID, "sub_ln": source.Name, "country": "NL",
		"registry_uri": trustBase + "/registry", "support_uri": trustBase + "/support",
		"srv_description":       []any{description},
		"supervisory_authority": map[string]any{},
		"entitlements":          []string{entitlement},
		"provides_attestations": provides,
		"public_body":           true,
		"iat":                   now.Unix(),
		// The NL Wallet refuses a WRPRC valid for more than twelve months.
		"exp": expires.Unix(),
		// The form of ETSI TS 119 475 Annex C, which both the NL Wallet and the
		// EUDI wallet read.
		"status":             map[string]any{"status_list": map[string]any{"idx": idx, "uri": trustBase + "/" + wrprcStatusListPath}},
		"policy_id":          []string{wrprcPolicyIdentifier},
		"certificate_policy": trustBase + "/certificate-policy",
	}
}

// signRegistrationCertificate signs a WRPRC as a JAdES B-B JWT: typ
// rc-wrp+jwt, x5c of the signer and iat in the protected header.
func signRegistrationCertificate(signer issuedCertificate, payload map[string]any, now time.Time) (string, error) {
	opts := (&jose.SignerOptions{}).WithType(wrprcJWTType).
		WithHeader("x5c", x5cHeader(signer.chain)).WithHeader("iat", now.Unix())
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: signer.key}, opts)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	jws, err := s.Sign(body)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

// publishTrust writes what the trust host serves: an empty CRL of the reader
// root, which the WRPACs point to, and the WRPRC status list with every
// registration valid. Revocation is not supported: every run publishes both
// afresh.
func publishTrust(options walletRegistrationOptions, reader, status issuedCertificate, trustBase string, now time.Time) error {
	if err := os.MkdirAll(filepath.Join(options.publishDir, filepath.Dir(wrprcStatusListPath)), 0o755); err != nil {
		return err
	}
	crl, err := emptyCRL(options.random, reader, now)
	if err != nil {
		return err
	}
	if err := writeFileAtomically(options.publishDir, wrpacCRLName, crl, 0o644); err != nil {
		return err
	}
	token, err := statuslist.Sign(statuslist.Token{
		URI: trustBase + "/" + wrprcStatusListPath, IssuedAt: now, Expires: earliest(now.Add(walletRegistrationValid), status.chain[0].NotAfter),
		TTL: 24 * time.Hour, Size: wrprcStatusListSize,
	}, status.key, x5cHeader(status.chain))
	if err != nil {
		return err
	}
	dir, name := filepath.Split(filepath.Join(options.publishDir, wrprcStatusListPath))
	return writeFileAtomically(dir, name, []byte(token), 0o644)
}

// emptyCRL signs a CRL that revokes nothing, valid as long as the
// registrations.
func emptyCRL(random io.Reader, ca issuedCertificate, now time.Time) ([]byte, error) {
	// The pre-provisioned reader root has no key usage extension, which
	// RFC 5280 section 4.2.1.3 reads as every use allowed. Go only signs a
	// CRL for an issuer that asserts cRLSign, so it gets a copy that does;
	// the CRL carries the root's own name and key identifier.
	issuer := *ca.chain[0]
	issuer.KeyUsage |= x509.KeyUsageCRLSign
	return x509.CreateRevocationList(random, &x509.RevocationList{
		Number: big.NewInt(now.Unix()), ThisUpdate: now.Add(-5 * time.Minute),
		NextUpdate: earliest(now.Add(walletRegistrationValid), ca.chain[0].NotAfter),
	}, &issuer, ca.key)
}

func x5cHeader(chain []*x509.Certificate) []string {
	out := make([]string, 0, len(chain))
	for _, c := range chain {
		out = append(out, base64.StdEncoding.EncodeToString(c.Raw))
	}
	return out
}

func writeKeyAndCertificate(dir, keyPath, certPath string, key *ecdsa.PrivateKey, chain [][]byte) error {
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := writeFileAtomically(dir, filepath.Base(keyPath), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	var certs []byte
	for _, der := range chain {
		certs = append(certs, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	return writeFileAtomically(dir, filepath.Base(certPath), certs, 0o644)
}

// ensureRequestEncryptionKey keeps the source's request encryption key or
// creates one. Wallets encrypt their credential requests to it on the own
// issuer's eudi profile (OpenID4VCI 1.0 section 10); it needs no
// certificate, as it is published in the signed issuer metadata.
func ensureRequestEncryptionKey(options walletRegistrationOptions, dir string) error {
	path := filepath.Join(dir, requestEncryptionKeyFile)
	if fileExists(path) {
		key, err := readECKey(path)
		if err != nil {
			return fmt.Errorf("load the request encryption key: %w", err)
		}
		if key.Curve != elliptic.P256() {
			return fmt.Errorf("the request encryption key %s is not a P-256 key; remove it to create a new one", path)
		}
		return nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), options.random)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writeFileAtomically(dir, requestEncryptionKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
}

func readECKey(path string) (*ecdsa.PrivateKey, error) {
	keyPEM, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("key is not ECDSA")
	}
	return key, nil
}

func readKeyAndChain(keyPath, certPath string) (issuedCertificate, error) {
	key, err := readECKey(keyPath)
	if err != nil {
		return issuedCertificate{}, err
	}
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return issuedCertificate{}, err
	}
	var chain []*x509.Certificate
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return issuedCertificate{}, err
		}
		chain = append(chain, c)
	}
	if len(chain) == 0 || !publicKeysEqual(&key.PublicKey, chain[0].PublicKey) {
		return issuedCertificate{}, errors.New("the key does not belong to the first certificate")
	}
	return issuedCertificate{key: key, chain: chain}, nil
}
