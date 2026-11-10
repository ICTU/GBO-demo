package main

import (
	"bytes"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The ASI authorization server as a relying party of the EUDI wallets
// (#639): it has the citizen log in by disclosing the PID through
// eudi-wallet-verifier. It is registered like a source, but only in the
// verifier role: a WRPAC under the reader root and a WRPRC with the
// Service_Provider entitlement for the PID claims of its login. This
// command also writes the verifier's configuration, its PID trust and the
// API key the authorization server uses.
const (
	asiVerifierID = "asi-authorization-server"
	// asiVerifierName is the name the wallet shows, as for the NL Wallet's
	// reader certificate of the same authorization server.
	asiVerifierName = "GBO demo ASIP (autorisatieserver)"
	// asiVerifierKvKNumber is fictional, as the demo sources' are.
	asiVerifierKvKNumber = "99991400"
	// asiVerifierDir holds the verifier's configuration; the verifier
	// mounts it. asiVerifierClientDir holds only the API key; the
	// authorization server mounts it.
	asiVerifierDir       = "asi-eudi-wallet-verifier"
	asiVerifierClientDir = "asi-authorization-server-eudi-wallet"
	asiVerifierConfig    = "config.json"
	asiVerifierAPIKey    = "api-key"
	asiVerifierUsecase   = "asi-login"
)

// asiLoginClaims are the PID claims the authorization server asks: the BSN
// and the mandatory PID attributes the NL PID has, as for the NL Wallet.
var asiLoginClaims = []string{"bsn", "family_name", "given_name", "birthdate", "nationalities"}

// qtspLoginUsecase is the reference QTSP's login through the same verifier
// (a demo shortcut, as with the NL Wallet's verifier): with the QTSP's own
// WRPAC, which the iOS test build already pins, and a WRPRC of its own.
const (
	qtspLoginUsecase = "qtsp-login"
	qtspLoginDir     = "qtsp-login"
)

// qtspLoginClaims are the PID claims the QTSP asks: not the BSN, which a
// QTSP may not process.
var qtspLoginClaims = []string{"family_name", "given_name", "birthdate", "nationalities"}

// qtspLoginPurpose is the purpose of the QTSP's NL Wallet login, also for
// the EUDI wallets (decided 2026-11-10).
var qtspLoginPurpose = []map[string]string{
	{"lang": "nl", "value": "Inloggen bij de vertrouwensdienst om een verklaring aan te vragen"},
	{"lang": "en", "value": "Log in at the trust service provider to request an attestation"},
}

// asiVerifierPurpose is the purpose the wallet shows (decided 2026-11-09).
var asiVerifierPurpose = []map[string]string{
	{"lang": "nl", "value": "Uw identiteit vaststellen, zodat u toestemming kunt geven voor het opvragen van uw gegevens bij de bron."},
	{"lang": "en", "value": "Establish your identity, so that you can consent to your data being requested from the source."},
}

type asiVerifierOptions struct {
	walletRegistrationOptions
	// publicURL is where wallets reach the verifier; authorizationServerURL
	// the authorization server's public URL: an https one gets same-device
	// returns.
	publicURL, authorizationServerURL string
	pidTrustAnchorsFile               string
}

func parseASIVerifierOptions(command string, arguments []string, stderr io.Writer) (asiVerifierOptions, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(stderr)
	options := asiVerifierOptions{walletRegistrationOptions: walletRegistrationOptions{now: time.Now, random: rand.Reader}}
	set.StringVar(&options.secretsDir, "secrets-dir", getEnv("ONBOARDING_SECRETS_DIR", ".local/secrets"), "filesystem secret directory")
	set.StringVar(&options.publishDir, "publish-dir", getEnv("EUDI_TRUST_PUBLISH_DIR", ".local/public-trust"), "directory the trust host serves")
	set.StringVar(&options.trustPublicURL, "trust-public-url", os.Getenv("EUDI_TRUST_PUBLIC_URL"), "public root URL of the trust host")
	set.StringVar(&options.kvkNumber, "kvk-number", asiVerifierKvKNumber, "fictional KvK number of the authorization server as relying party")
	set.StringVar(&options.publicURL, "public-url", "", "public URL of eudi-wallet-verifier")
	set.StringVar(&options.authorizationServerURL, "authorization-server-url", "", "public URL of the authorization server")
	set.StringVar(&options.pidTrustAnchorsFile, "pid-trust-anchors", getEnv("PID_TRUST_ANCHORS_FILE", ""), "NL PID issuer CAs, base64 DER, one per line")
	if err := set.Parse(arguments); err != nil {
		return options, err
	}
	if set.NArg() != 0 {
		return options, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	return options, nil
}

// provisionASIWalletVerifier registers the authorization server as relying
// party and writes eudi-wallet-verifier's configuration. A second run keeps
// the keys, the API key and the KvK number.
func provisionASIWalletVerifier(options asiVerifierOptions) error {
	public, err := url.Parse(options.publicURL)
	if err != nil || (public.Scheme != "https" && public.Scheme != "http") || public.Host == "" {
		return fmt.Errorf("the verifier's public URL %q is not an http(s) URL", options.publicURL)
	}
	as, err := url.Parse(options.authorizationServerURL)
	if err != nil || (as.Scheme != "https" && as.Scheme != "http") || as.Host == "" {
		return fmt.Errorf("the authorization server's public URL %q is not an http(s) URL", options.authorizationServerURL)
	}
	source := walletRegistrationSource{SourceID: asiVerifierID, Name: asiVerifierName, CertificateSet: asiVerifierDir}
	r, err := beginRegistration(options.walletRegistrationOptions, source)
	if err != nil {
		return err
	}
	idx, err := r.state.statusIndex(asiVerifierID + "/verifier")
	if err != nil {
		return err
	}
	if err := writeWalletRegistrationState(r.caDir, r.state); err != nil {
		return err
	}
	payload := verifierRegistrationPayload(source, r.orgID, r.trustBase, idx, r.now, r.expires, asiLoginClaims, asiVerifierPurpose)
	payload["srv_description"] = []any{
		map[string]string{"lang": "nl", "value": "Inloggen en toestemming geven voor het opvragen van gegevens bij een bron"},
		map[string]string{"lang": "en", "value": "Logging in and consenting to data being requested from a source"},
	}
	vrc, err := signRegistrationCertificate(r.signer, payload, r.now)
	if err != nil {
		return err
	}
	if err := writeFileAtomically(r.dir, verifierRegistrationFile, []byte(vrc), 0o644); err != nil {
		return err
	}
	anchors, akis, err := writeASIVerifierPIDTrust(options, r.dir)
	if err != nil {
		return err
	}
	if err := writeASIVerifierAPIKey(options); err != nil {
		return err
	}
	returnURL := "neither"
	if as.Scheme == "https" {
		returnURL = "same_device"
	}
	usecases := map[string]any{asiVerifierUsecase: map[string]any{
		"key_file": "wrpac-key.pem", "chain_file": "wrpac-chain.pem", "registration_certificate_file": verifierRegistrationFile,
		"pid_type": nlPIDType, "claims": asiLoginClaims, "return_url": returnURL,
	}}
	if registered, err := provisionQTSPLogin(options, r.dir); err != nil {
		return err
	} else if registered {
		usecases[qtspLoginUsecase] = map[string]any{
			"key_file": qtspLoginDir + "/wrpac-key.pem", "chain_file": qtspLoginDir + "/wrpac-chain.pem",
			"registration_certificate_file": qtspLoginDir + "/" + verifierRegistrationFile,
			"pid_type":                      nlPIDType, "claims": qtspLoginClaims, "return_url": "neither",
		}
	}
	config := map[string]any{
		"public_url":              strings.TrimRight(options.publicURL, "/"),
		"api_key_file":            asiVerifierAPIKey,
		"pid_anchors":             anchors,
		"pid_trusted_authorities": akis,
		"usecases":                usecases,
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomically(r.dir, asiVerifierConfig, raw, 0o600); err != nil {
		return err
	}
	return publishTrust(options.walletRegistrationOptions, r.reader, r.status, r.trustBase, r.now)
}

// writeASIVerifierPIDTrust writes the PID issuers the verifier trusts, as
// the issuer trusts them for disclosure-based issuance: the NL PID issuer
// CAs, and the test PID root when the test PID is provisioned. The
// trusted authorities are the roots' key identifiers.
func writeASIVerifierPIDTrust(options asiVerifierOptions, dir string) ([]map[string]string, []string, error) {
	nl, err := readBase64Certificates(options.pidTrustAnchorsFile)
	if err != nil {
		return nil, nil, fmt.Errorf("NL PID issuers (--pid-trust-anchors): %w", err)
	}
	sets := []struct {
		name  string
		roots []*x509.Certificate
	}{{"nl", nl}}
	testPIDDir := filepath.Join(options.secretsDir, testPIDID)
	if fileExists(testPIDDir) {
		root, err := readCertificateFile(filepath.Join(testPIDDir, testPIDRootFile))
		if err != nil {
			return nil, nil, fmt.Errorf("test PID root: %w; run make provision-test-pid", err)
		}
		sets = append(sets, struct {
			name  string
			roots []*x509.Certificate
		}{testPIDID, []*x509.Certificate{root}})
	}
	if err := ensurePrivateDirectory(filepath.Join(dir, "pid-trust")); err != nil {
		return nil, nil, err
	}
	anchors := []map[string]string{}
	akis := []string{}
	for _, s := range sets {
		var b bytes.Buffer
		for _, root := range s.roots {
			if err := pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}); err != nil {
				return nil, nil, err
			}
			if len(root.SubjectKeyId) > 0 {
				akis = append(akis, base64.RawURLEncoding.EncodeToString(root.SubjectKeyId))
			}
		}
		name := "pid-trust/" + s.name + ".pem"
		if err := writeFileAtomically(filepath.Join(dir, "pid-trust"), s.name+".pem", b.Bytes(), 0o644); err != nil {
			return nil, nil, err
		}
		anchors = append(anchors, map[string]string{"name": s.name, "roots_file": name})
	}
	return anchors, akis, nil
}

// writeASIVerifierAPIKey keeps the API key of the verifier's internal API,
// or makes one, and writes it for the verifier and for the authorization
// server, each in its own directory.
func writeASIVerifierAPIKey(options asiVerifierOptions) error {
	clientDir := filepath.Join(options.secretsDir, asiVerifierClientDir)
	key, err := os.ReadFile(filepath.Join(clientDir, asiVerifierAPIKey))
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(bytes.TrimSpace(key)) == 0) {
		b := make([]byte, 32)
		if _, err := io.ReadFull(options.random, b); err != nil {
			return err
		}
		key = []byte(base64.RawURLEncoding.EncodeToString(b))
	} else if err != nil {
		return err
	}
	key = bytes.TrimSpace(key)
	for _, dir := range []string{clientDir, filepath.Join(options.secretsDir, asiVerifierDir)} {
		if err := ensurePrivateDirectory(dir); err != nil {
			return err
		}
		if err := writeFileAtomically(dir, asiVerifierAPIKey, key, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// provisionQTSPLogin gives the reference QTSP's login a usecase of the
// verifier when the QTSP is registered (make
// provision-qtsp-wallet-registration): its WRPAC, kept as it is, copied
// into the verifier's directory, and a WRPRC as Service_Provider for the
// claims of its login, as a private party. The WRPRC stays out of the
// QTSP's own directory, which the issuer's materializer reads.
func provisionQTSPLogin(options asiVerifierOptions, verifierDir string) (bool, error) {
	if !fileExists(filepath.Join(options.secretsDir, qtspCertificateSetID, "wrpac-chain.pem")) {
		return false, nil
	}
	qtsp := options.walletRegistrationOptions
	// The QTSP keeps the KvK number it was registered with.
	qtsp.kvkNumber = ""
	source := walletRegistrationSource{SourceID: qtspCertificateSetID, Name: qtspName, CertificateSet: qtspCertificateSetID}
	r, err := beginRegistration(qtsp, source)
	if err != nil {
		return false, fmt.Errorf("reference QTSP: %w", err)
	}
	idx, err := r.state.statusIndex(qtspCertificateSetID + "/verifier")
	if err != nil {
		return false, err
	}
	if err := writeWalletRegistrationState(r.caDir, r.state); err != nil {
		return false, err
	}
	payload := verifierRegistrationPayload(source, r.orgID, r.trustBase, idx, r.now, r.expires, qtspLoginClaims, qtspLoginPurpose)
	payload["public_body"] = false
	payload["srv_description"] = []any{
		map[string]string{"lang": "nl", "value": "Inloggen bij de vertrouwensdienst om een verklaring aan te vragen"},
		map[string]string{"lang": "en", "value": "Logging in at the trust service provider to request an attestation"},
	}
	vrc, err := signRegistrationCertificate(r.signer, payload, r.now)
	if err != nil {
		return false, err
	}
	dir := filepath.Join(verifierDir, qtspLoginDir)
	if err := ensurePrivateDirectory(dir); err != nil {
		return false, err
	}
	for _, name := range []string{"wrpac-key.pem", "wrpac-chain.pem"} {
		body, err := os.ReadFile(filepath.Join(r.dir, name))
		if err != nil {
			return false, err
		}
		if err := writeFileAtomically(dir, name, body, 0o600); err != nil {
			return false, err
		}
	}
	return true, writeFileAtomically(dir, verifierRegistrationFile, []byte(vrc), 0o644)
}
