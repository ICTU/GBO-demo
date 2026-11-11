package main

import (
	"crypto/rand"
	"encoding/json"
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

// The reference QTSP as a provider of the own credential issuer: it issues
// its test statement through a pre-authorized offer it makes itself, under
// the issuer's combined profile. It is not part of the Source Release; its
// issuer and status certificates and its callback come from
// provision-qtsp-issuance-certificates.
const (
	qtspName      = "GBO demo test QTSP"
	qtspKvKNumber = "99991300"
	// wrprcQEAAEntitlement is the QTSP's role: it issues (test) statements
	// as a qualified trust service provider would.
	wrprcQEAAEntitlement = "https://uri.etsi.org/19475/Entitlement/QEAA_Provider"
	// qtspConfigurationID is the QTSP's credential configuration in the
	// issuer; the QTSP names it when it asks for an offer.
	qtspConfigurationID = qtspCertificateSetID + "-attestation"
	// qtspValidDays is how long the statement is valid, as the QTSP issues
	// it through eudi-issuance-server.
	qtspValidDays = 30
)

func parseQTSPWalletRegistrationOptions(command string, arguments []string, stderr io.Writer) (walletRegistrationOptions, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(stderr)
	options := walletRegistrationOptions{now: time.Now, random: rand.Reader}
	set.StringVar(&options.secretsDir, "secrets-dir", getEnv("ONBOARDING_SECRETS_DIR", ".local/secrets"), "filesystem secret directory")
	set.StringVar(&options.publishDir, "publish-dir", getEnv("EUDI_TRUST_PUBLISH_DIR", ".local/public-trust"), "directory the trust host serves")
	set.StringVar(&options.trustPublicURL, "trust-public-url", os.Getenv("EUDI_TRUST_PUBLIC_URL"), "public root URL of the trust host")
	set.StringVar(&options.kvkNumber, "kvk-number", getEnv("QTSP_KVK_NUMBER", qtspKvKNumber), "fictional KvK number of the reference QTSP")
	if err := set.Parse(arguments); err != nil {
		return options, err
	}
	if set.NArg() != 0 {
		return options, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	return options, nil
}

// provisionQTSPWalletRegistration registers the reference QTSP like a
// source: a WRPAC under the reader root and a WRPRC with the QEAA_Provider
// entitlement for its statement, and a request encryption key. It asks for
// no PID, so it has no verifier WRPRC. Its certificate set must exist.
func provisionQTSPWalletRegistration(options walletRegistrationOptions) error {
	available, err := qtspMaterialAvailable(options.secretsDir)
	if err != nil {
		return err
	}
	if !available {
		return errors.New("the reference QTSP has no certificates; run make provision-qtsp-issuance-certificates first")
	}
	return registerWallet(options, walletRegistrationSource{
		SourceID: qtspCertificateSetID, Name: qtspName, CertificateSet: qtspCertificateSetID, VCTs: []string{qtspVCT},
	}, wrprcQEAAEntitlement, false)
}

// qtspProvider is the reference QTSP as a provider of the issuer, when its
// wallet registration exists; without it the issuer runs without the QTSP.
// Its bronpoort is the QTSP's callback over the QTSP's own TLS
// certificate, which the development issuer CA signs.
func qtspProvider(options credentialIssuerOptions, seen derivedNames) (issuerProviderFile, bool, error) {
	dir := filepath.Join(options.secretsDir, qtspCertificateSetID)
	if !fileExists(filepath.Join(dir, "wrpac-chain.pem")) {
		return issuerProviderFile{}, false, nil
	}
	if earlier, ok := seen.vcts[qtspVCT]; ok {
		return issuerProviderFile{}, false, fmt.Errorf("vct %s is activated for %s as well as for the reference QTSP", qtspVCT, earlier)
	}
	if available, err := qtspMaterialAvailable(options.secretsDir); err != nil {
		return issuerProviderFile{}, false, fmt.Errorf("the reference QTSP is registered, but %w", err)
	} else if !available {
		return issuerProviderFile{}, false, errors.New("the reference QTSP is registered, but its certificate set is missing; run make provision-qtsp-issuance-certificates")
	}
	provider := issuerProviderFile{Name: qtspCertificateSetID}
	const provision = "make provision-qtsp-wallet-registration"
	if err := copyWalletRegistration(options, qtspCertificateSetID, qtspCertificateSetID, []string{qtspVCT}, &provider, provision); err != nil {
		return provider, false, err
	}
	artifacts := certificateArtifactsForMountedSecret(options.secretsDir, qtspCertificateSetID)
	var err error
	if provider.Issuer, err = writeLeafAsPEM(options, qtspCertificateSetID, "issuer", artifacts.IssuerKeyReference, artifacts.IssuerCertReference); err != nil {
		return provider, false, err
	}
	if provider.Status, err = writeLeafAsPEM(options, qtspCertificateSetID, "status", artifacts.StatusKeyReference, artifacts.StatusCertReference); err != nil {
		return provider, false, err
	}
	callbackURL, err := qtspIssuerCallbackURL(options, dir)
	if err != nil {
		return provider, false, err
	}
	caFile, err := writeQTSPCallbackCA(options)
	if err != nil {
		return provider, false, err
	}
	display, claims, err := qtspTypeMetadata(options.qtspMetadataPath)
	if err != nil {
		return provider, false, err
	}
	provider.CredentialConfigurations = []issuerConfigurationFile{{
		ID: qtspConfigurationID, VCT: qtspVCT, Scope: qtspConfigurationID, Display: display, Claims: claims,
		ValidDays: qtspValidDays, StatusList: "attestation", SourceURL: callbackURL, SourceCAFile: caFile,
	}}
	return provider, true, nil
}

func qtspIssuerCallbackURL(options credentialIssuerOptions, dir string) (string, error) {
	body, err := os.ReadFile(filepath.Join(dir, qtspCallbackPathFile))
	if err != nil {
		return "", fmt.Errorf("read the reference QTSP callback path: %w", err)
	}
	token, err := parseQTSPCallbackToken(string(body))
	if err != nil {
		return "", err
	}
	if _, err := url.Parse(options.qtspAttestationURL); err != nil {
		return "", err
	}
	return qtspCallbackURL(options.qtspAttestationURL, token)
}

// writeQTSPCallbackCA writes the development issuer CA, which signs the
// QTSP's TLS certificate, as the trust anchor of its callback.
func writeQTSPCallbackCA(options credentialIssuerOptions) (string, error) {
	body, err := os.ReadFile(filepath.Join(options.secretsDir, "development-ca", "issuer-ca-cert.pem"))
	if err != nil {
		return "", fmt.Errorf("the development issuer CA that signs the QTSP callback: %w", err)
	}
	if _, err := readCertificateFile(filepath.Join(options.secretsDir, "development-ca", "issuer-ca-cert.pem")); err != nil {
		return "", err
	}
	name := qtspCertificateSetID + "/callback-ca.pem"
	return name, writeFileAtomically(filepath.Join(options.outputDir, qtspCertificateSetID), "callback-ca.pem", body, 0o644)
}

// qtspTypeMetadata reads the display and claims of the QTSP statement from
// its Type Metadata; every claim is mandatory, as the QTSP always sends it.
func qtspTypeMetadata(path string) ([]issuerDisplay, []issuerClaim, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read QTSP Type Metadata: %w", err)
	}
	var md struct {
		VCT     string `json:"vct"`
		Display []struct {
			Lang string `json:"lang"`
			Name string `json:"name"`
		} `json:"display"`
		Claims []struct {
			Path    []string `json:"path"`
			Display []struct {
				Lang  string `json:"lang"`
				Label string `json:"label"`
			} `json:"display"`
		} `json:"claims"`
	}
	if err := json.Unmarshal(body, &md); err != nil || md.VCT != qtspVCT || len(md.Display) == 0 || len(md.Claims) == 0 {
		return nil, nil, fmt.Errorf("QTSP Type Metadata must be JSON for VCT %s with display and claims", qtspVCT)
	}
	display := make([]issuerDisplay, 0, len(md.Display))
	for _, d := range md.Display {
		display = append(display, issuerDisplay{Name: d.Name, Locale: d.Lang})
	}
	claims := make([]issuerClaim, 0, len(md.Claims))
	for _, c := range md.Claims {
		claim := issuerClaim{Path: c.Path, Mandatory: true}
		for _, d := range c.Display {
			claim.Display = append(claim.Display, issuerDisplay{Name: d.Label, Locale: d.Lang})
		}
		claims = append(claims, claim)
	}
	return display, claims, nil
}
