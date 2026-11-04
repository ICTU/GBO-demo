package main

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Disclosure-based issuance in the own credential issuer: per source the
// WRPRC of its verifier role, per source offer a disclosure offer, and the
// PID issuers the issuer trusts.

// disclosureOfferKey is what the issuer takes as an offer key: one path
// segment of the request_uri.
var disclosureOfferKey = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)

// disclosureOffer turns a source offer of credential configuration
// configurationID into a disclosure offer under the same key, with its
// parameters as the text the bronpoort query carries.
func disclosureOffer(sourceID, configurationID string, o sourceOffer) (issuerDisclosureOffer, error) {
	if !disclosureOfferKey.MatchString(o.ID) {
		return issuerDisclosureOffer{}, fmt.Errorf("offer %q: the issuer takes only letters, digits, hyphens, underscores and single inner dots as an offer key", o.ID)
	}
	out := issuerDisclosureOffer{Key: o.ID, Provider: sourceID, CredentialConfigurationIDs: []string{configurationID}}
	for name, value := range o.Parameters {
		text, err := canonicalOfferParameter(value)
		if err != nil {
			return issuerDisclosureOffer{}, fmt.Errorf("offer %q parameter %q: %w", o.ID, name, err)
		}
		if out.Parameters == nil {
			out.Parameters = map[string]string{}
		}
		out.Parameters[name] = text
	}
	return out, nil
}

// copyVerifierRegistration copies the WRPRC of the source's verifier role
// and names it in provider, after the checks the issuer and the NL Wallet
// would make.
func copyVerifierRegistration(options credentialIssuerOptions, name, certificateSet string, provider *issuerProviderFile, provision string) error {
	from := filepath.Join(options.secretsDir, certificateSet)
	raw, err := os.ReadFile(filepath.Join(from, verifierRegistrationFile))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no verifier registration (%s); run %s", verifierRegistrationFile, provision)
	}
	if err != nil {
		return err
	}
	if err := checkVerifierRegistration(from, raw, options.now()); err != nil {
		return fmt.Errorf("verifier registration: %w; run %s again", err, provision)
	}
	if err := writeFileAtomically(filepath.Join(options.outputDir, name), "verifier-registration-certificate.jwt", raw, 0o644); err != nil {
		return err
	}
	provider.VerifierRegistrationCertificateFile = name + "/verifier-registration-certificate.jwt"
	return nil
}

// checkVerifierRegistration refuses a verifier WRPRC that is expired, bound
// to another access certificate, not for the Service_Provider role or not
// authorizing the BSN of the PID.
func checkVerifierRegistration(dir string, raw []byte, now time.Time) error {
	wrpac, err := readKeyAndChain(filepath.Join(dir, "wrpac-key.pem"), filepath.Join(dir, "wrpac-chain.pem"))
	if err != nil {
		return fmt.Errorf("access certificate: %w", err)
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), ".")
	if len(parts) != 3 {
		return errors.New("not a compact JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var rc struct {
		Sub          string   `json:"sub"`
		Exp          int64    `json:"exp"`
		Entitlements []string `json:"entitlements"`
		Credentials  []struct {
			Format string `json:"format"`
			Meta   struct {
				VCTValues []string `json:"vct_values"`
			} `json:"meta"`
			Claim []struct {
				Path   []any `json:"path"`
				Values []any `json:"values"`
			} `json:"claim"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(payload, &rc); err != nil {
		return err
	}
	if rc.Sub == "" || rc.Sub != certificateOrganizationIdentifier(wrpac.chain[0]) {
		return errors.New("it does not belong to the access certificate")
	}
	if !now.Before(time.Unix(rc.Exp, 0)) {
		return errors.New("it has expired")
	}
	if !slices.Contains(rc.Entitlements, wrprcServiceProviderEntitlement) {
		return errors.New("it is not for the Service_Provider role")
	}
	for _, c := range rc.Credentials {
		if c.Format != "dc+sd-jwt" || !slices.Contains(c.Meta.VCTValues, nlPIDType) {
			continue
		}
		for _, cl := range c.Claim {
			if len(cl.Path) == 1 && cl.Path[0] == "bsn" && len(cl.Values) == 0 {
				return nil
			}
		}
	}
	return errors.New("it does not authorize the bsn of the PID")
}

// pidTrustAnchors writes the PID issuers the issuer trusts: the NL PID
// issuer CAs from pidTrustAnchorsFile, and the test PID root when the test
// PID is provisioned.
func pidTrustAnchors(options credentialIssuerOptions) ([]issuerAnchors, error) {
	roots, err := readBase64Certificates(options.pidTrustAnchorsFile)
	if err != nil {
		return nil, fmt.Errorf("NL PID issuers (PID_TRUST_ANCHORS_FILE): %w", err)
	}
	anchors := []issuerAnchors{}
	if err := writePIDRoots(options, "nl", roots); err != nil {
		return nil, err
	}
	anchors = append(anchors, issuerAnchors{Name: "nl", RootsFile: "pid-trust/nl.pem"})
	dir := filepath.Join(options.secretsDir, testPIDID)
	if !fileExists(dir) {
		return anchors, nil
	}
	root, err := readCertificateFile(filepath.Join(dir, testPIDRootFile))
	if err != nil {
		return nil, fmt.Errorf("test PID root: %w; run make provision-test-pid", err)
	}
	if !options.now().Before(root.NotAfter) {
		return nil, errors.New("the test PID root has expired; run make provision-test-pid again")
	}
	if err := writePIDRoots(options, testPIDID, []*x509.Certificate{root}); err != nil {
		return nil, err
	}
	return append(anchors, issuerAnchors{Name: testPIDID, RootsFile: "pid-trust/" + testPIDID + ".pem"}), nil
}

func writePIDRoots(options credentialIssuerOptions, name string, roots []*x509.Certificate) error {
	var b bytes.Buffer
	for _, r := range roots {
		if err := pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: r.Raw}); err != nil {
			return err
		}
	}
	dir := filepath.Join(options.outputDir, "pid-trust")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomically(dir, name+".pem", b.Bytes(), 0o644)
}

// readBase64Certificates reads certificates as base64 DER, one per line;
// empty lines and lines starting with # are skipped.
func readBase64Certificates(path string) ([]*x509.Certificate, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*x509.Certificate
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		der, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			return nil, fmt.Errorf("%s: a line is not base64: %w", path, err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, cert)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no certificates", path)
	}
	return out, nil
}

func readCertificateFile(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("%s: no certificate", path)
	}
	return x509.ParseCertificate(block.Bytes)
}
