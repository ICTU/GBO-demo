package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// The test relying parties of the developer portal (GBO-demo#737): two
// fictional parties registered as Service_Provider for every attestation
// type of the own credential issuer, with all its claims, so the portal
// can ask a wallet for any of them and show which copy was presented to
// whom.
const testRPDir = "test-relying-party"

type testRelyingParty struct{ id, name, kvk string }

var testRelyingParties = []testRelyingParty{
	{"a", "GBO testpartij A", "99991501"},
	{"b", "GBO testpartij B", "99991502"},
}

var testRPPurpose = []map[string]string{
	{"lang": "nl", "value": "Demonstratie van het gebruik van exemplaren van een attestatie"},
	{"lang": "en", "value": "Demonstrating how the copies of an attestation are used"},
}

type testRPOptions struct {
	walletRegistrationOptions
	publicURL, issuerURL string
	providers            []string
	// explicitProviders tells that the providers were named on purpose:
	// then a provider without metadata is an error, not skipped.
	explicitProviders bool
	issuerCAFile      string
	testPIDCAFile     string
	client            *http.Client
}

func parseTestRPOptions(command string, arguments []string, stderr io.Writer) (testRPOptions, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(stderr)
	options := testRPOptions{walletRegistrationOptions: walletRegistrationOptions{now: time.Now, random: rand.Reader},
		client: &http.Client{Timeout: 10 * time.Second}}
	publicCerts := getEnv("ONBOARDING_PUBLIC_CERTIFICATES_DIR", ".local/public-certificates")
	var providers string
	set.StringVar(&options.secretsDir, "secrets-dir", getEnv("ONBOARDING_SECRETS_DIR", ".local/secrets"), "filesystem secret directory")
	set.StringVar(&options.publishDir, "publish-dir", getEnv("EUDI_TRUST_PUBLISH_DIR", ".local/public-trust"), "directory the trust host serves")
	set.StringVar(&options.trustPublicURL, "trust-public-url", os.Getenv("EUDI_TRUST_PUBLIC_URL"), "public root URL of the trust host")
	set.StringVar(&options.publicURL, "public-url", os.Getenv("TEST_RP_PUBLIC_URL"), "public URL of the test relying party")
	set.StringVar(&options.issuerURL, "issuer-url", "http://localhost:"+getEnv("GBO_PORT_CREDENTIAL_ISSUER", "9429"), "URL where this command reads the issuer metadata")
	set.StringVar(&providers, "providers", "test-pid,belastingdienst,rvig,demo-unsecured", "providers of the own credential issuer, comma-separated; without it, those of these that the issuer has")
	set.StringVar(&options.issuerCAFile, "issuer-ca", filepath.Join(publicCerts, "development-ca", "issuer-ca-cert.pem"), "issuer CA of the sources")
	set.StringVar(&options.testPIDCAFile, "test-pid-ca", filepath.Join(publicCerts, "development-ca", "test-pid-ca-cert.pem"), "root of the test PID")
	if err := set.Parse(arguments); err != nil {
		return options, err
	}
	if set.NArg() != 0 {
		return options, fmt.Errorf("unexpected positional arguments: %s", strings.Join(set.Args(), " "))
	}
	set.Visit(func(f *flag.Flag) { options.explicitProviders = options.explicitProviders || f.Name == "providers" })
	for _, p := range strings.Split(providers, ",") {
		if p = strings.TrimSpace(p); p != "" {
			options.providers = append(options.providers, p)
		}
	}
	return options, nil
}

// testRPCredential is an attestation type as the test relying party's
// configuration holds it.
type testRPCredential struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	VCT    string     `json:"vct"`
	Claims [][]string `json:"claims"`
}

// provisionTestRelyingParties registers the test relying parties and
// writes the configuration of test-relying-party. It reads the attestation
// types from the issuer's metadata, so the issuer must run; after a new
// type, run it again. A second run keeps the keys and KvK numbers. It
// returns the providers it skipped because the issuer has no metadata for
// them.
func provisionTestRelyingParties(options testRPOptions) ([]string, error) {
	public, err := url.Parse(options.publicURL)
	if err != nil || (public.Scheme != "https" && public.Scheme != "http") || public.Host == "" {
		return nil, fmt.Errorf("the test relying party's public URL %q is not an http(s) URL", options.publicURL)
	}
	creds, skipped, err := issuerCredentials(options)
	if err != nil {
		return skipped, err
	}
	base := filepath.Join(options.secretsDir, testRPDir)
	if err := ensurePrivateDirectory(base); err != nil {
		return skipped, err
	}
	var parties []map[string]string
	var r registration
	for _, p := range testRelyingParties {
		source := walletRegistrationSource{SourceID: testRPDir + "-" + p.id, Name: p.name, CertificateSet: testRPDir + "/" + p.id}
		opts := options.walletRegistrationOptions
		opts.kvkNumber = p.kvk
		if r, err = beginRegistration(opts, source); err != nil {
			return skipped, err
		}
		idx, err := r.state.statusIndex(source.SourceID + "/verifier")
		if err != nil {
			return skipped, err
		}
		if err := writeWalletRegistrationState(r.caDir, r.state); err != nil {
			return skipped, err
		}
		payload := testRPRegistrationPayload(source, r.orgID, r.trustBase, idx, r.now, r.expires, creds)
		vrc, err := signRegistrationCertificate(r.signer, payload, r.now)
		if err != nil {
			return skipped, err
		}
		if err := writeFileAtomically(r.dir, verifierRegistrationFile, []byte(vrc), 0o644); err != nil {
			return skipped, err
		}
		parties = append(parties, map[string]string{
			"id": p.id, "name": p.name, "key_file": p.id + "/wrpac-key.pem", "chain_file": p.id + "/wrpac-chain.pem",
			"registration_certificate_file": p.id + "/" + verifierRegistrationFile,
		})
	}
	anchors := []map[string]string{}
	for name, from := range map[string]string{"gbo-sources": options.issuerCAFile, "test-pid": options.testPIDCAFile} {
		body, err := os.ReadFile(from)
		if errors.Is(err, os.ErrNotExist) && name == "test-pid" {
			continue // the test PID is not provisioned
		} else if err != nil {
			return skipped, fmt.Errorf("issuer anchors %s: %w", name, err)
		}
		file := name + "-ca.pem"
		if err := writeFileAtomically(base, file, body, 0o644); err != nil {
			return skipped, err
		}
		anchors = append(anchors, map[string]string{"name": name, "certificates_file": file})
	}
	sort.Slice(anchors, func(i, j int) bool { return anchors[i]["name"] < anchors[j]["name"] })
	config := map[string]any{
		"public_url":      strings.TrimRight(options.publicURL, "/"),
		"relying_parties": parties,
		"credentials":     creds,
		"issuer_anchors":  anchors,
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return skipped, err
	}
	if err := writeFileAtomically(base, "config.json", raw, 0o644); err != nil {
		return skipped, err
	}
	return skipped, publishTrust(options.walletRegistrationOptions, r.reader, r.status, r.trustBase, r.now)
}

// testRPRegistrationPayload is the WRPRC of a test relying party: a
// Service_Provider that may ask for every claim of every type in creds.
func testRPRegistrationPayload(source walletRegistrationSource, orgID, trustBase string, idx int, now, expires time.Time, creds []testRPCredential) map[string]any {
	payload := registrationPayload(source, wrprcServiceProviderEntitlement, orgID, trustBase, idx, now, expires)
	delete(payload, "provides_attestations")
	payload["id"] = "gbo-" + source.SourceID + "-verifier"
	payload["public_body"] = false
	payload["srv_description"] = []any{testRPPurpose}
	// One entry per vct: two configurations of one type share their claims.
	byVCT := map[string][][]string{}
	var order []string
	for _, c := range creds {
		if _, ok := byVCT[c.VCT]; !ok {
			order = append(order, c.VCT)
		}
		for _, p := range c.Claims {
			if !slices.ContainsFunc(byVCT[c.VCT], func(q []string) bool { return slices.Equal(p, q) }) {
				byVCT[c.VCT] = append(byVCT[c.VCT], p)
			}
		}
	}
	var credentials []map[string]any
	for _, vct := range order {
		paths := make([]map[string]any, len(byVCT[vct]))
		for i, p := range byVCT[vct] {
			paths[i] = map[string]any{"path": p}
		}
		// NL Wallet v0.6 names the claims of a credential "claim".
		credentials = append(credentials, map[string]any{"format": "dc+sd-jwt", "meta": map[string]any{"vct_values": []string{vct}}, "claim": paths})
	}
	payload["credentials"] = credentials
	payload["purpose"] = testRPPurpose
	return payload
}

// issuerCredentials reads the attestation types and their claims from the
// issuer metadata of each provider's eudi profile. A provider the issuer
// does not have (404) is skipped and returned, unless the providers were
// named on purpose.
func issuerCredentials(options testRPOptions) ([]testRPCredential, []string, error) {
	var out []testRPCredential
	var skipped []string
	for _, p := range options.providers {
		u := strings.TrimRight(options.issuerURL, "/") + "/.well-known/openid-credential-issuer/" + url.PathEscape(p) + "/eudi"
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, skipped, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := options.client.Do(req)
		if err != nil {
			return nil, skipped, fmt.Errorf("issuer metadata of %s: %w (is the issuer running?)", p, err)
		}
		if resp.StatusCode == http.StatusNotFound && !options.explicitProviders {
			resp.Body.Close()
			skipped = append(skipped, p)
			continue
		}
		var md struct {
			Configurations map[string]struct {
				VCT      string `json:"vct"`
				Metadata struct {
					Display []struct {
						Name string `json:"name"`
					} `json:"display"`
					Claims []struct {
						Path []string `json:"path"`
					} `json:"claims"`
				} `json:"credential_metadata"`
			} `json:"credential_configurations_supported"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&md)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			return nil, skipped, fmt.Errorf("issuer metadata of %s: HTTP %d, %v", p, resp.StatusCode, err)
		}
		ids := make([]string, 0, len(md.Configurations))
		for id := range md.Configurations {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			c := md.Configurations[id]
			cred := testRPCredential{ID: id, Name: id, VCT: c.VCT}
			if len(c.Metadata.Display) > 0 && c.Metadata.Display[0].Name != "" {
				cred.Name = c.Metadata.Display[0].Name
			}
			for _, cl := range c.Metadata.Claims {
				if len(cl.Path) > 0 {
					cred.Claims = append(cred.Claims, cl.Path)
				}
			}
			if c.VCT != "" && len(cred.Claims) > 0 {
				out = append(out, cred)
			}
		}
	}
	if len(out) == 0 {
		return nil, skipped, errors.New("the issuer metadata names no attestation types with claims")
	}
	return out, skipped, nil
}
