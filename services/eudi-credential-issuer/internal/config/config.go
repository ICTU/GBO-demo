// Package config reads the issuer's configuration file: profiles,
// credential configurations and the files holding keys and certificates.
// Process settings (URLs, ports, database) come from the environment; see
// main.
package config

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/x5c"
)

// File is the JSON configuration file. Relative paths are resolved against
// the directory of the file.
type File struct {
	Issuer                   KeyFiles                           `json:"issuer"`
	Status                   KeyFiles                           `json:"status"`
	StatusListSize           int                                `json:"status_list_size"`
	BatchSize                int                                `json:"batch_size"`
	OfferTTL                 Duration                           `json:"offer_ttl"`
	TokenTTL                 Duration                           `json:"token_ttl"`
	NonceTTL                 Duration                           `json:"nonce_ttl"`
	Profiles                 []ProfileFile                      `json:"profiles"`
	CredentialConfigurations []issuance.CredentialConfiguration `json:"-"`
	// SourceEndpoints maps each credential configuration to the bronpoort
	// URL that serves it (source_url in the file).
	SourceEndpoints map[string]string `json:"-"`
}

// KeyFiles names a PEM private key and a PEM certificate chain, leaf first.
type KeyFiles struct {
	KeyFile   string `json:"key_file"`
	ChainFile string `json:"chain_file"`
}

// ProfileFile configures one profile.
type ProfileFile struct {
	Name string        `json:"name"`
	Kind issuance.Kind `json:"kind"`
	// WalletProviderRootsFile holds the PEM roots of trusted wallet
	// providers (Wallet Instance Attestations).
	WalletProviderRootsFile string `json:"wallet_provider_roots_file"`
	// KeyAttestationRootsFile holds the PEM roots for key attestations.
	KeyAttestationRootsFile string   `json:"key_attestation_roots_file"`
	Access                  KeyFiles `json:"access"`
	// RegistrationCertificateFile holds the WRPRC as raw bytes.
	RegistrationCertificateFile string `json:"registration_certificate_file"`
	UniversalLinkBase           string `json:"universal_link_base"`
	// KeyStorage and UserAuthentication are the levels a key attestation
	// must assert (OpenID4VCI Appendix D.2), for example "iso_18045_high".
	KeyStorage         []string `json:"key_storage"`
	UserAuthentication []string `json:"user_authentication"`
}

// Duration reads a Go duration string such as "15m".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}

// credentialConfigurationFile is issuance.CredentialConfiguration with
// JSON names, plus the bronpoort URL that belongs to the source adapter.
type credentialConfigurationFile struct {
	ID         string             `json:"id"`
	VCT        string             `json:"vct"`
	Scope      string             `json:"scope"`
	Display    []issuance.Display `json:"display"`
	Claims     []issuance.Claim   `json:"claims"`
	ValidDays  int                `json:"valid_days"`
	StatusList string             `json:"status_list"`
	SourceURL  string             `json:"source_url"`
}

// UnmarshalJSON reads credential configurations in their file shape.
func (f *File) UnmarshalJSON(b []byte) error {
	type plain File
	var aux struct {
		plain
		CredentialConfigurations []credentialConfigurationFile `json:"credential_configurations"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	*f = File(aux.plain)
	f.SourceEndpoints = map[string]string{}
	for _, c := range aux.CredentialConfigurations {
		f.CredentialConfigurations = append(f.CredentialConfigurations, issuance.CredentialConfiguration{
			ID: c.ID, VCT: c.VCT, Scope: c.Scope, Display: c.Display, Claims: c.Claims,
			ValidDays: c.ValidDays, StatusList: c.StatusList,
		})
		f.SourceEndpoints[c.ID] = c.SourceURL
	}
	return nil
}

// Loaded is the configuration of the issuer core and of the source
// adapter.
type Loaded struct {
	Issuer issuance.Config
	// SourceEndpoints maps credential configuration ids to bronpoort URLs.
	SourceEndpoints map[string]string
}

// Load reads the file at path and returns the configuration for publicURL.
func Load(path, publicURL string) (Loaded, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Loaded{}, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return Loaded{}, fmt.Errorf("parse %s: %w", path, err)
	}
	for id, u := range f.SourceEndpoints {
		if u == "" {
			return Loaded{}, fmt.Errorf("credential configuration %q: source_url is required", id)
		}
	}
	cfg, err := load(f, filepath.Dir(path), publicURL)
	if err != nil {
		return Loaded{}, err
	}
	return Loaded{Issuer: cfg, SourceEndpoints: f.SourceEndpoints}, nil
}

func load(f File, dir, publicURL string) (issuance.Config, error) {
	var err error
	cfg := issuance.Config{
		PublicURL: publicURL, Configurations: f.CredentialConfigurations,
		StatusListSize: f.StatusListSize, BatchSize: f.BatchSize,
		OfferTTL: time.Duration(f.OfferTTL), TokenTTL: time.Duration(f.TokenTTL), NonceTTL: time.Duration(f.NonceTTL),
	}
	if cfg.IssuerKey, cfg.IssuerChain, err = loadKeyFiles(dir, f.Issuer); err != nil {
		return issuance.Config{}, fmt.Errorf("issuer: %w", err)
	}
	if cfg.StatusKey, cfg.StatusChain, err = loadKeyFiles(dir, f.Status); err != nil {
		return issuance.Config{}, fmt.Errorf("status: %w", err)
	}
	for _, pf := range f.Profiles {
		p := issuance.Profile{Name: pf.Name, Kind: pf.Kind, UniversalLinkBase: pf.UniversalLinkBase,
			KeyStorage: pf.KeyStorage, UserAuthentication: pf.UserAuthentication}
		if p.AccessKey, p.AccessChain, err = loadKeyFiles(dir, pf.Access); err != nil {
			return issuance.Config{}, fmt.Errorf("profile %s access certificate: %w", pf.Name, err)
		}
		if p.WalletProviders, err = loadAnchors(dir, pf.WalletProviderRootsFile, pf.Name+" wallet providers"); err != nil {
			return issuance.Config{}, fmt.Errorf("profile %s: %w", pf.Name, err)
		}
		if pf.KeyAttestationRootsFile != "" {
			if p.KeyAttestationProviders, err = loadAnchors(dir, pf.KeyAttestationRootsFile, pf.Name+" key attestation"); err != nil {
				return issuance.Config{}, fmt.Errorf("profile %s: %w", pf.Name, err)
			}
		}
		if pf.RegistrationCertificateFile != "" {
			raw, err := os.ReadFile(resolve(dir, pf.RegistrationCertificateFile))
			if err != nil {
				return issuance.Config{}, fmt.Errorf("profile %s registration certificate: %w", pf.Name, err)
			}
			// A file usually ends with a newline; the certificate does not.
			p.RegistrationCertificate = bytes.TrimSpace(raw)
		}
		cfg.Profiles = append(cfg.Profiles, p)
	}
	return cfg, nil
}

func resolve(dir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(dir, path)
}

func loadKeyFiles(dir string, k KeyFiles) (crypto.Signer, []*x509.Certificate, error) {
	if k.KeyFile == "" || k.ChainFile == "" {
		return nil, nil, errors.New("key_file and chain_file are required")
	}
	key, err := readKey(resolve(dir, k.KeyFile))
	if err != nil {
		return nil, nil, err
	}
	chain, err := readCertificates(resolve(dir, k.ChainFile))
	if err != nil {
		return nil, nil, err
	}
	if !publicKeysEqual(key.Public(), chain[0].PublicKey) {
		return nil, nil, errors.New("the key does not belong to the first certificate of the chain")
	}
	return key, chain, nil
}

func loadAnchors(dir, path, name string) ([]x5c.Anchors, error) {
	if path == "" {
		return nil, fmt.Errorf("%s: a roots file is required", name)
	}
	roots, err := readCertificates(resolve(dir, path))
	if err != nil {
		return nil, err
	}
	return []x5c.Anchors{x5c.NewAnchors(name, roots...)}, nil
}

func readKey(path string) (crypto.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	var key any
	switch block.Type {
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("%s: unsupported PEM block %q", path, block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%s: not a signing key", path)
	}
	return signer, nil
}

func readCertificates(path string) ([]*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []*x509.Certificate
	for {
		var block *pem.Block
		block, raw = pem.Decode(raw)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no certificates", path)
	}
	return out, nil
}

func publicKeysEqual(a, b crypto.PublicKey) bool {
	type equaler interface{ Equal(crypto.PublicKey) bool }
	e, ok := a.(equaler)
	return ok && e.Equal(b)
}
