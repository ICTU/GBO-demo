package main

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gbo-demo/eudi-adapter/internal/onboarding"
	"gbo-demo/eudi-adapter/internal/postgresregistry"
)

// Configuration of the own credential issuer (services/eudi-credential-issuer)
// from the active Source Release: per source an Attestation Provider with its
// certificates and wallet registration, per activated type a credential
// configuration, and the wallet profiles whose trusted wallet providers are
// mounted. The JSON mirrors the issuer's configuration file.

const (
	credentialIssuerConfigFile = "issuer.json"
	// credentialValidDays and the issuer's default batch size of four match
	// the attestation settings of the current issuance server.
	credentialValidDays     = 365
	nlWalletProvidersFile   = "nl-wallet-providers.pem"
	eudiWalletProvidersFile = "eudi-wallet-providers.pem"
	eudiKeyAttestationFile  = "eudi-key-attestation-roots.pem"
)

// issuerNamePattern is what the issuer accepts as a provider, profile or
// status list name: one path segment.
var issuerNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

type credentialIssuerOptions struct {
	secretsDir, outputDir, adapterBaseURL string
	// rootsDir holds the trusted wallet providers per profile; a profile is
	// configured only when its file is there.
	rootsDir, universalLinkBase string
	// eudiKeyStorage and eudiUserAuthentication are the key attestation
	// levels the eudi profile requires (ARF ISSU_27d).
	eudiKeyStorage, eudiUserAuthentication []string
	// testPIDSourceURL is the bronpoort of the test PID (test-pid-source).
	testPIDSourceURL string
	// pidTrustAnchorsFile holds the NL PID issuer CAs whose PIDs the issuer
	// accepts (base64 DER per line, # comments), next to the test PID.
	pidTrustAnchorsFile string
	now                 func() time.Time
	// owner, when set, becomes the owner of everything written: the issuer
	// runs as another user than this command.
	owner *fileOwner
}

type fileOwner struct{ uid, gid int }

// parseFileOwner reads "uid" or "uid:gid".
func parseFileOwner(value string) (*fileOwner, error) {
	if value == "" {
		return nil, nil
	}
	uidText, gidText, hasGID := strings.Cut(value, ":")
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid < 0 {
		return nil, fmt.Errorf("--owner %q: want uid or uid:gid", value)
	}
	gid := uid
	if hasGID {
		if gid, err = strconv.Atoi(gidText); err != nil || gid < 0 {
			return nil, fmt.Errorf("--owner %q: want uid or uid:gid", value)
		}
	}
	return &fileOwner{uid: uid, gid: gid}, nil
}

type issuerFile struct {
	Providers        []issuerProviderFile    `json:"providers"`
	Profiles         []issuerProfileFile     `json:"profiles"`
	DisclosureOffers []issuerDisclosureOffer `json:"disclosure_offers,omitempty"`
	PIDTrustAnchors  []issuerAnchors         `json:"pid_trust_anchors,omitempty"`
}

// issuerDisclosureOffer is a source offer the wallet starts by disclosing
// the BSN from its PID.
type issuerDisclosureOffer struct {
	Key                        string            `json:"key"`
	Provider                   string            `json:"provider"`
	CredentialConfigurationIDs []string          `json:"credential_configuration_ids"`
	Parameters                 map[string]string `json:"parameters,omitempty"`
}

type issuerAnchors struct {
	Name      string `json:"name"`
	RootsFile string `json:"roots_file"`
}

type issuerKeyFiles struct {
	KeyFile   string `json:"key_file"`
	ChainFile string `json:"chain_file"`
}

type issuerProviderFile struct {
	Name                        string         `json:"name"`
	Access                      issuerKeyFiles `json:"access"`
	Issuer                      issuerKeyFiles `json:"issuer"`
	Status                      issuerKeyFiles `json:"status"`
	RegistrationCertificateFile string         `json:"registration_certificate_file"`
	// VerifierRegistrationCertificateFile is the WRPRC of the provider as a
	// relying party that asks for the PID; the test PID has none.
	VerifierRegistrationCertificateFile string                    `json:"verifier_registration_certificate_file,omitempty"`
	RequestEncryptionKeyFile            string                    `json:"request_encryption_key_file"`
	CredentialConfigurations            []issuerConfigurationFile `json:"credential_configurations"`
}

type issuerDisplay struct {
	Name   string `json:"name"`
	Locale string `json:"locale"`
}

type issuerClaim struct {
	Path      []string        `json:"path"`
	Mandatory bool            `json:"mandatory,omitempty"`
	Display   []issuerDisplay `json:"display,omitempty"`
}

type issuerConfigurationFile struct {
	ID         string          `json:"id"`
	VCT        string          `json:"vct"`
	Scope      string          `json:"scope"`
	Display    []issuerDisplay `json:"display"`
	Claims     []issuerClaim   `json:"claims"`
	ValidDays  int             `json:"valid_days"`
	StatusList string          `json:"status_list"`
	SourceURL  string          `json:"source_url"`
}

type issuerProfileFile struct {
	Name                    string   `json:"name"`
	Kind                    string   `json:"kind"`
	WalletProviderRootsFile string   `json:"wallet_provider_roots_file"`
	KeyAttestationRootsFile string   `json:"key_attestation_roots_file,omitempty"`
	UniversalLinkBase       string   `json:"universal_link_base,omitempty"`
	KeyStorage              []string `json:"key_storage,omitempty"`
	UserAuthentication      []string `json:"user_authentication,omitempty"`
}

func runCredentialIssuerConfigCommand(ctx context.Context, arguments []string, stdout, stderr io.Writer) (bool, error) {
	if len(arguments) == 0 || arguments[0] != "materialize-credential-issuer" {
		return false, nil
	}
	set := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
	set.SetOutput(stderr)
	var options credentialIssuerOptions
	databaseURL := set.String("database-url", os.Getenv("SOURCE_REGISTRY_DATABASE_URL"), "read-only PostgreSQL Source Registry connection URL")
	databaseSchema := set.String("database-schema", getEnv("SOURCE_REGISTRY_SCHEMA", "source_registry"), "PostgreSQL Source Registry schema")
	set.StringVar(&options.secretsDir, "secrets-dir", getEnv("ONBOARDING_SECRETS_DIR", "/source-certificates"), "mounted source certificate Secret directory")
	set.StringVar(&options.outputDir, "output-dir", "/runtime-issuer", "pod-local directory for the issuer configuration, owned by this command")
	set.StringVar(&options.adapterBaseURL, "adapter-base-url", os.Getenv("EUDI_BRI_URL"), "public GBO adapter base URL")
	set.StringVar(&options.rootsDir, "wallet-roots-dir", "/wallet-roots", "directory with the trusted wallet providers per profile")
	set.StringVar(&options.pidTrustAnchorsFile, "pid-trust-anchors-file", getEnv("PID_TRUST_ANCHORS_FILE", "/pid-trust-anchors.txt"), "NL PID issuer CAs the issuer trusts for disclosure-based issuance, base64 DER per line")
	set.StringVar(&options.testPIDSourceURL, "test-pid-source-url", getEnv("TEST_PID_SOURCE_URL", "http://test-pid-source:8080/attestations"), "bronpoort of the test PID, used when the test PID is provisioned")
	set.StringVar(&options.universalLinkBase, "universal-link-base", getEnv("ISSUER_NL_UNIVERSAL_LINK_BASE", "walletdebuginteraction://wallet.edi.rijksoverheid.nl/"), "base of the NL Wallet universal link")
	owner := set.String("owner", os.Getenv("ISSUER_RUNTIME_OWNER"), "uid or uid:gid that becomes the owner of the output, for an issuer running as another user")
	keyStorage := set.String("eudi-key-storage", os.Getenv("ISSUER_EUDI_KEY_STORAGE"), "comma-separated key storage levels the eudi profile requires")
	userAuthentication := set.String("eudi-user-authentication", os.Getenv("ISSUER_EUDI_USER_AUTHENTICATION"), "comma-separated user authentication levels the eudi profile requires")
	if err := set.Parse(arguments[1:]); err != nil {
		return true, err
	}
	if set.NArg() != 0 {
		return true, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	options.eudiKeyStorage, options.eudiUserAuthentication = splitList(*keyStorage), splitList(*userAuthentication)
	options.now = time.Now
	var err error
	if options.owner, err = parseFileOwner(*owner); err != nil {
		return true, err
	}
	store, err := postgresregistry.Open(ctx, postgresregistry.Options{DatabaseURL: *databaseURL, Schema: *databaseSchema})
	if err != nil {
		return true, err
	}
	defer store.Close()
	release, err := store.ActiveRelease(ctx)
	if err != nil {
		return true, err
	}
	if err := materializeCredentialIssuer(options, release); err != nil {
		return true, fmt.Errorf("materialize credential issuer: %w", err)
	}
	_, _ = fmt.Fprintf(stdout, "credential issuer configuration materialized from Source Release %s\n", release.ID)
	return true, nil
}

func splitList(value string) []string {
	var out []string
	for _, v := range strings.Split(value, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func materializeCredentialIssuer(options credentialIssuerOptions, release onboarding.SourceRelease) error {
	adapterBaseURL, err := parseAdapterBaseURL(options.adapterBaseURL)
	if err != nil {
		return err
	}
	if err := validateIssuanceRelease(release); err != nil {
		return err
	}
	if options.now == nil {
		options.now = time.Now
	}
	if err := os.MkdirAll(options.outputDir, 0o700); err != nil {
		return fmt.Errorf("create credential issuer runtime directory: %w", err)
	}
	// Everything is written to a staging directory first and replaces the
	// output only when complete: a failed run leaves the previous, consistent
	// configuration, and keys of sources that left the release go.
	outputDir := options.outputDir
	staging, err := os.MkdirTemp(outputDir, ".staging-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	options.outputDir = staging
	if err := writeCredentialIssuer(options, release, adapterBaseURL); err != nil {
		return err
	}
	if options.owner != nil {
		if err := chownTree(staging, *options.owner); err != nil {
			return fmt.Errorf("hand the output to %d:%d: %w", options.owner.uid, options.owner.gid, err)
		}
	}
	return replaceDirectoryContents(outputDir, staging)
}

func writeCredentialIssuer(options credentialIssuerOptions, release onboarding.SourceRelease, adapterBaseURL *url.URL) error {
	profiles, err := credentialIssuerProfiles(options)
	if err != nil {
		return err
	}
	out := issuerFile{Profiles: profiles}
	seen := derivedNames{ids: map[string]string{}, vcts: map[string]string{}}
	for _, source := range release.Sources {
		provider, offers, err := credentialIssuerProvider(options, source, adapterBaseURL, seen)
		if err != nil {
			return fmt.Errorf("source %s: %w", source.SourceID, err)
		}
		if len(provider.CredentialConfigurations) > 0 {
			out.Providers = append(out.Providers, provider)
			out.DisclosureOffers = append(out.DisclosureOffers, offers...)
		}
	}
	if len(out.Providers) == 0 {
		return errors.New("active Source Release has no source with activated types")
	}
	if testPID, ok, err := testPIDProvider(options, seen); err != nil {
		return fmt.Errorf("test PID: %w", err)
	} else if ok {
		out.Providers = append(out.Providers, testPID)
	}
	if out.PIDTrustAnchors, err = pidTrustAnchors(options); err != nil {
		return fmt.Errorf("PID trust anchors: %w", err)
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomically(options.outputDir, credentialIssuerConfigFile, append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("write credential issuer configuration: %w", err)
	}
	return writeReleaseMarker(options.outputDir, release)
}

// replaceDirectoryContents makes dir hold exactly what staging holds.
func replaceDirectoryContents(dir, staging string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if p := filepath.Join(dir, e.Name()); p != staging {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
		}
	}
	staged, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, e := range staged {
		if err := os.Rename(filepath.Join(staging, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// chownTree gives everything under root, root excluded, to owner.
func chownTree(root string, owner fileOwner) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		return os.Lchown(path, owner.uid, owner.gid)
	})
}

// derivedNames remembers which source, type and version gave each
// configuration id and vct.
type derivedNames struct{ ids, vcts map[string]string }

// issuerName is a name the issuer takes for a provider or status list: one
// path segment, not the reserved route "status".
func issuerName(name string) bool {
	return issuerNamePattern.MatchString(name) && name != "status"
}

// rootsMounted reports whether a roots file is there; an unreadable one is
// an error, not a missing profile.
func rootsMounted(path string) (bool, error) {
	_, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// credentialIssuerProfiles configures nl and eudi when their trusted wallet
// providers are mounted, and refuses when neither is.
func credentialIssuerProfiles(options credentialIssuerOptions) ([]issuerProfileFile, error) {
	var profiles []issuerProfileFile
	mounted := map[string]bool{}
	for _, name := range []string{nlWalletProvidersFile, eudiWalletProvidersFile, eudiKeyAttestationFile} {
		ok, err := rootsMounted(filepath.Join(options.rootsDir, name))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		mounted[name] = ok
	}
	if mounted[nlWalletProvidersFile] {
		roots, err := copyRoots(options, nlWalletProvidersFile)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, issuerProfileFile{Name: "nl", Kind: "nl", WalletProviderRootsFile: roots, UniversalLinkBase: options.universalLinkBase})
	}
	if mounted[eudiWalletProvidersFile] {
		if len(options.eudiKeyStorage) == 0 || len(options.eudiUserAuthentication) == 0 {
			return nil, errors.New("the eudi profile needs the key storage and user authentication levels a key attestation must assert (ARF ISSU_27d)")
		}
		roots, err := copyRoots(options, eudiWalletProvidersFile)
		if err != nil {
			return nil, err
		}
		// The key attestations come from the wallet provider unless their
		// roots are mounted separately.
		attestationRoots := roots
		if mounted[eudiKeyAttestationFile] {
			if attestationRoots, err = copyRoots(options, eudiKeyAttestationFile); err != nil {
				return nil, err
			}
		}
		profiles = append(profiles, issuerProfileFile{Name: "eudi", Kind: "eudi", WalletProviderRootsFile: roots,
			KeyAttestationRootsFile: attestationRoots, KeyStorage: options.eudiKeyStorage, UserAuthentication: options.eudiUserAuthentication})
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no wallet profile: mount %s or %s in %s", nlWalletProvidersFile, eudiWalletProvidersFile, options.rootsDir)
	}
	return profiles, nil
}

func copyRoots(options credentialIssuerOptions, name string) (string, error) {
	body, err := os.ReadFile(filepath.Join(options.rootsDir, name))
	if err != nil {
		return "", err
	}
	// The issuer reads every block, so every block must be a certificate.
	count := 0
	for rest := body; len(bytes.TrimSpace(rest)) > 0; count++ {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" {
			return "", fmt.Errorf("%s must hold only PEM certificates", name)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
	}
	if count == 0 {
		return "", fmt.Errorf("%s holds no certificate", name)
	}
	return name, writeFileAtomically(options.outputDir, name, body, 0o644)
}

// credentialIssuerProvider is one source as an Attestation Provider: its
// mounted certificates, checked against the release, its wallet
// registration and a credential configuration per activated type.
func credentialIssuerProvider(options credentialIssuerOptions, source onboarding.ReleaseSource, adapterBaseURL *url.URL, seen derivedNames) (issuerProviderFile, []issuerDisclosureOffer, error) {
	if !issuerName(source.SourceID) {
		return issuerProviderFile{}, nil, errors.New("the issuer takes only lower-case letters, digits and hyphens, and not \"status\", as a provider name")
	}
	if source.SourceID == testPIDID {
		return issuerProviderFile{}, nil, fmt.Errorf("%q is reserved for the test PID provider", testPIDID)
	}
	activation, err := activationFromRegistryCandidate(onboarding.SourceCandidate{
		SourceID: source.SourceID, Snapshot: source.Snapshot,
		CertificateSet: source.CertificateSet, TypeMetadata: source.TypeMetadata,
	})
	if err != nil {
		return issuerProviderFile{}, nil, fmt.Errorf("activation: %w", err)
	}
	provider := issuerProviderFile{Name: source.SourceID}
	var offers []issuerDisclosureOffer
	if len(activation.Types) == 0 {
		return provider, nil, nil
	}
	artifacts := certificateArtifactsForMountedSecret(options.secretsDir, source.CertificateSet.ID)
	mountedPublic, err := publicCertificateSet(source.CertificateSet.ID, artifacts)
	if err != nil {
		return provider, nil, fmt.Errorf("public certificates: %w", err)
	}
	if !reflect.DeepEqual(mountedPublic, source.CertificateSet) {
		return provider, nil, errors.New("mounted certificate material does not match the active release")
	}
	vcts := make([]string, 0, len(activation.Types))
	for _, t := range activation.Types {
		vcts = append(vcts, t.VCT)
	}
	provision := "make provision-wallet-registration SOURCE_ID=" + source.SourceID
	if err := copyWalletRegistration(options, source.SourceID, source.CertificateSet.ID, vcts, &provider, provision); err != nil {
		return provider, nil, err
	}
	if err := copyVerifierRegistration(options, source.SourceID, source.CertificateSet.ID, &provider, provision); err != nil {
		return provider, nil, err
	}
	if provider.Issuer, err = writeLeafAsPEM(options, source.SourceID, "issuer", artifacts.IssuerKeyReference, artifacts.IssuerCertReference); err != nil {
		return provider, nil, err
	}
	if provider.Status, err = writeLeafAsPEM(options, source.SourceID, "status", artifacts.StatusKeyReference, artifacts.StatusCertReference); err != nil {
		return provider, nil, err
	}
	for _, t := range activation.Types {
		if !issuerName(t.TypeID) {
			return provider, nil, fmt.Errorf("type %s: the issuer takes only lower-case letters, digits and hyphens, and not \"status\", as a status list name", t.TypeID)
		}
		origin := source.SourceID + "/" + t.TypeID + " version " + t.TypeVersion
		id := source.SourceID + "-" + t.TypeID
		if earlier, ok := seen.ids[id]; ok {
			return provider, nil, fmt.Errorf("credential configuration %s is derived from both %s and %s", id, earlier, origin)
		}
		seen.ids[id] = origin
		if earlier, ok := seen.vcts[t.VCT]; ok {
			return provider, nil, fmt.Errorf("vct %s is activated for both %s and %s", t.VCT, earlier, origin)
		}
		seen.vcts[t.VCT] = origin
		endpoint := *adapterBaseURL
		endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/attestations/" + source.SourceID + "/" + t.TypeID
		provider.CredentialConfigurations = append(provider.CredentialConfigurations, issuerConfigurationFile{
			ID: id, VCT: t.VCT, Scope: id, Display: issuerDisplays(t.Definition.Display), Claims: issuerClaims(t.Definition.Claims),
			ValidDays: credentialValidDays, StatusList: t.TypeID, SourceURL: endpoint.String(),
		})
		for _, o := range t.Offers {
			offer, err := disclosureOffer(source.SourceID, id, o)
			if err != nil {
				return provider, nil, fmt.Errorf("type %s: %w", t.TypeID, err)
			}
			offers = append(offers, offer)
		}
	}
	return provider, offers, nil
}

// copyWalletRegistration copies the WRPAC, the WRPRC and the request
// encryption key that provisioning made for a provider, and names them in
// provider; provision is the command that makes them.
func copyWalletRegistration(options credentialIssuerOptions, name, certificateSet string, vcts []string, provider *issuerProviderFile, provision string) error {
	from := filepath.Join(options.secretsDir, certificateSet)
	// Provisioned name to runtime name; keys stay private.
	files := []struct {
		from, to string
		mode     os.FileMode
	}{
		{"wrpac-key.pem", "access-key.pem", 0o600},
		{"wrpac-chain.pem", "access-chain.pem", 0o644},
		{"wrprc.jwt", "registration-certificate.jwt", 0o644},
		{requestEncryptionKeyFile, "request-encryption-key.pem", 0o600},
	}
	for _, f := range files {
		if !fileExists(filepath.Join(from, f.from)) {
			return fmt.Errorf("no wallet registration (%s); run %s", f.from, provision)
		}
	}
	if err := checkWalletRegistration(from, vcts, options.now()); err != nil {
		return fmt.Errorf("%w; run %s again", err, provision)
	}
	dir := filepath.Join(options.outputDir, name)
	if err := ensurePrivateDirectory(dir); err != nil {
		return err
	}
	for _, f := range files {
		body, err := os.ReadFile(filepath.Join(from, f.from))
		if err != nil {
			return err
		}
		if err := writeFileAtomically(dir, f.to, body, f.mode); err != nil {
			return err
		}
	}
	provider.Access = issuerKeyFiles{KeyFile: name + "/access-key.pem", ChainFile: name + "/access-chain.pem"}
	provider.RegistrationCertificateFile = name + "/registration-certificate.jwt"
	provider.RequestEncryptionKeyFile = name + "/request-encryption-key.pem"
	return nil
}

// checkWalletRegistration refuses a WRPAC or WRPRC the issuer or a wallet
// would refuse: expired, not bound to each other, or not naming every
// activated type (the registration predates a newly activated type).
func checkWalletRegistration(dir string, vcts []string, now time.Time) error {
	wrpac, err := readKeyAndChain(filepath.Join(dir, "wrpac-key.pem"), filepath.Join(dir, "wrpac-chain.pem"))
	if err != nil {
		return fmt.Errorf("access certificate: %w", err)
	}
	if !now.Before(wrpac.chain[0].NotAfter) {
		return errors.New("the access certificate has expired")
	}
	if key, err := readECKey(filepath.Join(dir, requestEncryptionKeyFile)); err != nil || key.Curve != elliptic.P256() {
		return errors.New("the request encryption key is not a P-256 key")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "wrprc.jwt"))
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), ".")
	if len(parts) != 3 {
		return errors.New("the registration certificate is not a compact JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("registration certificate: %w", err)
	}
	var rc struct {
		Sub                  string `json:"sub"`
		Exp                  int64  `json:"exp"`
		ProvidesAttestations []struct {
			Meta struct {
				VCTValues []string `json:"vct_values"`
			} `json:"meta"`
		} `json:"provides_attestations"`
	}
	if err := json.Unmarshal(payload, &rc); err != nil {
		return fmt.Errorf("registration certificate: %w", err)
	}
	if rc.Sub == "" || rc.Sub != certificateOrganizationIdentifier(wrpac.chain[0]) {
		return errors.New("the registration certificate does not belong to the access certificate")
	}
	if !now.Before(time.Unix(rc.Exp, 0)) {
		return errors.New("the registration certificate has expired")
	}
	var provided []string
	for _, a := range rc.ProvidesAttestations {
		provided = append(provided, a.Meta.VCTValues...)
	}
	for _, vct := range vcts {
		if !slices.Contains(provided, vct) {
			return fmt.Errorf("the registration certificate does not name the activated type %s", vct)
		}
	}
	return nil
}

func certificateOrganizationIdentifier(c *x509.Certificate) string {
	for _, n := range c.Subject.Names {
		if n.Type.Equal(organizationIdentifierOID) {
			if s, ok := n.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

// writeLeafAsPEM writes a provisioned leaf key and certificate (base64 DER)
// as the PEM files the issuer reads. The chain is the leaf alone: the
// development CAs are roots, which HAIP keeps out of x5c.
func writeLeafAsPEM(options credentialIssuerOptions, sourceID, role, keyPath, certPath string) (issuerKeyFiles, error) {
	key, cert, err := loadDevelopmentLeaf(keyPath, certPath)
	if err != nil {
		return issuerKeyFiles{}, fmt.Errorf("%s certificate: %w", role, err)
	}
	dir := filepath.Join(options.outputDir, sourceID)
	if err := ensurePrivateDirectory(dir); err != nil {
		return issuerKeyFiles{}, err
	}
	keyFile, chainFile := role+"-key.pem", role+"-chain.pem"
	if err := writeKeyAndCertificate(dir, filepath.Join(dir, keyFile), filepath.Join(dir, chainFile), key, [][]byte{cert.Raw}); err != nil {
		return issuerKeyFiles{}, err
	}
	return issuerKeyFiles{KeyFile: sourceID + "/" + keyFile, ChainFile: sourceID + "/" + chainFile}, nil
}

func issuerDisplays(displays sourceDisplays) []issuerDisplay {
	out := make([]issuerDisplay, 0, len(displays))
	for _, d := range displays {
		out = append(out, issuerDisplay{Name: d.Name, Locale: d.Lang})
	}
	return out
}

// issuerClaims describes each claim as the Type Metadata does: required
// unless the source marks it optional, with its label per language.
func issuerClaims(claims sourceClaims) []issuerClaim {
	out := make([]issuerClaim, 0, len(claims))
	for _, c := range claims {
		claim := issuerClaim{Path: []string{c.Name}, Mandatory: !c.Source.Optional}
		for _, label := range c.Label {
			claim.Display = append(claim.Display, issuerDisplay{Name: label.Text, Locale: label.Lang})
		}
		out = append(out, claim)
	}
	return out
}
