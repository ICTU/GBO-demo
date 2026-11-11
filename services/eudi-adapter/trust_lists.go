package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gbo-demo/eudi-adapter/internal/lotefiles"
	"gbo-demo/eudi-adapter/internal/lotesigning"
	"gbo-demo/eudi-adapter/internal/nlconfig"
	"gbo-demo/eudi-adapter/internal/trustlists"
)

type trustListOptions struct {
	rootDir, publicDir, publishDir, secretsDir, publicURL string
	nlURL, nlKeyFile, nlPIDFingerprints                   string
	validDays                                             int
	renew                                                 bool
	trustNLHTTPS                                          bool
}

func runTrustListCommand(ctx context.Context, args []string, stdout, stderr io.Writer) (bool, error) {
	if len(args) == 0 || (args[0] != "provision-trust-list-signer" && args[0] != "publish-trust-lists") {
		return false, nil
	}
	set := flag.NewFlagSet(args[0], flag.ContinueOnError)
	set.SetOutput(stderr)
	var options trustListOptions
	trustHTTPS, err := strconv.ParseBool(getEnv("GBO_LOTE_TRUST_NL_HTTPS", "false"))
	if err != nil {
		return true, fmt.Errorf("GBO_LOTE_TRUST_NL_HTTPS: %w", err)
	}
	set.StringVar(&options.rootDir, "root-dir", getEnv("GBO_LOTE_SIGNER_DIR", ".local/lote-signer"), "host-only LoTE root and signer directory; never mount in a container")
	set.StringVar(&options.publicDir, "public-certificates-dir", getEnv("ONBOARDING_PUBLIC_CERTIFICATES_DIR", ".local/public-certificates"), "public-only GBO certificate projection")
	set.StringVar(&options.publishDir, "publish-dir", getEnv("EUDI_TRUST_PUBLISH_DIR", ".local/public-trust"), "trust host publication directory")
	set.StringVar(&options.secretsDir, "secrets-dir", getEnv("ONBOARDING_SECRETS_DIR", ".local/secrets"), "container-mounted certificate directory (for key isolation checks only)")
	set.StringVar(&options.publicURL, "trust-public-url", os.Getenv("EUDI_TRUST_PUBLIC_URL"), "public HTTPS trust-host URL")
	set.StringVar(&options.nlURL, "nl-config-url", getEnv("GBO_LOTE_NL_CONFIG_URL", nlconfig.DefaultURL), "NL pre-production wallet configuration URL")
	set.StringVar(&options.nlKeyFile, "nl-config-key-file", os.Getenv("GBO_LOTE_NL_CONFIG_KEY_FILE"), "trusted pre-production public key PEM, base64 DER or config-server-config.json")
	set.StringVar(&options.nlPIDFingerprints, "nl-pid-ca-sha256", getEnv("GBO_LOTE_NL_PID_CA_SHA256", nlconfig.DefaultPIDFingerprint), "comma-separated reviewed SHA-256 fingerprints of NL PID CA certificates")
	set.IntVar(&options.validDays, "valid-days", 30, "publication validity in days (1 to 30)")
	set.BoolVar(&options.renew, "renew", false, "publish again, with new times and sequence, even when content is unchanged")
	set.BoolVar(&options.trustNLHTTPS, "trust-nl-https", trustHTTPS, "demo shortcut: without a configured key, trust only the fixed official NL URL over verified HTTPS")
	if err := set.Parse(args[1:]); err != nil {
		return true, err
	}
	if set.NArg() != 0 {
		return true, errors.New("unexpected positional arguments")
	}
	if err := checkLoTEKeyDirectory(options); err != nil {
		return true, err
	}
	if args[0] == "provision-trust-list-signer" {
		if err := provisionTrustListSigner(options, time.Now()); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintln(stdout, "LoTE scheme root and signer provisioned on the host; only the root certificate is projected publicly")
		return true, nil
	}
	var key *ecdsa.PublicKey
	if options.nlKeyFile != "" {
		key, err = nlconfig.ReadKey(options.nlKeyFile)
		if err != nil {
			return true, err
		}
	} else if !options.trustNLHTTPS {
		return true, errors.New("GBO_LOTE_NL_CONFIG_KEY_FILE is required unless the explicit --trust-nl-https demo shortcut is enabled")
	}
	nlURL, err := url.Parse(options.nlURL)
	if err != nil || nlURL.Scheme != "https" || nlURL.Hostname() == "" || nlURL.User != nil || nlURL.RawQuery != "" || nlURL.Fragment != "" {
		return true, errors.New("NL config URL must be an absolute HTTPS URL without credentials, query or fragment")
	}
	if key == nil && options.nlURL != nlconfig.DefaultURL {
		return true, errors.New("HTTPS demo trust is restricted to the fixed official NL pre-production URL")
	}
	if key == nil {
		_, _ = fmt.Fprintln(stderr, "DEMO SHORTCUT: trusting the official NL HTTPS source; the NL configuration JWT signature is not verified")
	}
	now := time.Now()
	if options.validDays < 1 || options.validDays > 30 {
		return true, errors.New("valid-days must be between 1 and 30")
	}
	signer, err := readTrustListSigner(options, now, now.Add(time.Duration(options.validDays)*24*time.Hour))
	if err != nil {
		return true, err
	}
	roots := map[trustlists.Role]string{}
	for role, name := range map[trustlists.Role]string{trustlists.PID: "test-pid", trustlists.PubEAA: "issuer", trustlists.WRPAC: "reader", trustlists.WRPRC: "registrar"} {
		roots[role] = filepath.Join(options.publicDir, "development-ca", name+"-ca-cert.pem")
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 3 {
			return errors.New("NL configuration redirects must stay on HTTPS and be limited to three hops")
		}
		return nil
	}}
	source := lotefiles.Source{Roots: roots, NL: nlconfig.Source{URL: options.nlURL, Key: key,
		PIDFingerprints: strings.Split(options.nlPIDFingerprints, ","), Client: client, TrustOfficialHTTPS: options.trustNLHTTPS}}
	result, err := trustlists.Publish(ctx, trustlists.Options{PublicURL: options.publicURL,
		Validity: time.Duration(options.validDays) * 24 * time.Hour, Renew: options.renew, Now: now}, source, signer, lotefiles.Publications{Directory: options.publishDir})
	if err != nil {
		return true, err
	}
	_, _ = fmt.Fprintf(stdout, "LoTE changed=%t sequence=%d NL-config=%d NL-authentication=%s next-update=%s\n", result.Changed, result.Sequence, result.NLVersion, result.NLAuthentication, result.NextUpdate.Format(time.RFC3339))
	for _, role := range trustlists.Roles {
		_, _ = fmt.Fprintf(stdout, "%s: %d distinct anchors\n", role.Filename(), result.Counts[role])
	}
	return true, nil
}

func provisionTrustListSigner(options trustListOptions, now time.Time) error {
	if err := checkLoTEKeyDirectory(options); err != nil {
		return err
	}
	key, certificate := caFiles(options.rootDir, "lote")
	if !fileExists(key) && !fileExists(certificate) {
		if err := createDevelopmentRoot(options.rootDir, "lote", pkix.Name{CommonName: "GBO LoTE scheme operator root", Organization: []string{"GBO Demo"}, Country: []string{"NL"}}, now, rand.Reader); err != nil {
			return err
		}
	}
	root, err := loadRole(options.rootDir, "lote", "LoTE scheme operator root")
	if err != nil {
		return err
	}
	if root.chain[0].Subject.CommonName != "GBO LoTE scheme operator root" {
		return errors.New("LoTE root is not the dedicated scheme operator root")
	}
	_, err = ensureCertificate(walletRegistrationOptions{now: func() time.Time { return now }, random: rand.Reader}, options.rootDir,
		"signer", root, &x509.Certificate{Subject: pkix.Name{CommonName: "GBO LoTE list signer", Organization: []string{"GBO Demo"}, Country: []string{"NL"}},
			NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true})
	if err != nil {
		return err
	}
	return projectRoot(options.rootDir, "lote", filepath.Join(options.publicDir, "development-ca"))
}

func readTrustListSigner(options trustListOptions, now, until time.Time) (lotesigning.Signer, error) {
	var signer lotesigning.Signer
	leaf, err := readKeyAndChain(filepath.Join(options.rootDir, "signer-key.pem"), filepath.Join(options.rootDir, "signer-chain.pem"))
	if err != nil {
		return signer, fmt.Errorf("LoTE signer: %w; run make provision-trust-list-signer", err)
	}
	_, rootPath := caFiles(options.rootDir, "lote")
	der, err := lotefiles.ReadCertificate(rootPath)
	if err != nil {
		return signer, err
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		return signer, err
	}
	signer = lotesigning.Signer{Key: leaf.key, Certificate: leaf.chain[0], Root: root}
	return signer, signer.Validate(now, until)
}

func checkLoTEKeyDirectory(options trustListOptions) error {
	root, err := canonicalLoTEPath(options.rootDir)
	if err != nil {
		return err
	}
	for _, directory := range []string{options.publicDir, options.publishDir, options.secretsDir} {
		mounted, err := canonicalLoTEPath(directory)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(mounted, root)
		if err != nil {
			return err
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return fmt.Errorf("LoTE private directory %s must be outside container-mounted directory %s", root, mounted)
		}
	}
	return nil
}

// Resolve existing ancestors too: a non-existent child of a symlink must not
// hide a signer key inside a mounted secrets or public directory.
func canonicalLoTEPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("LoTE directories must not be empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent, err := canonicalLoTEPath(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}
