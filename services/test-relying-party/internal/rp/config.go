package rp

import (
	"crypto"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/x5c"
)

// File is the configuration file that `eudi-adapter
// provision-test-relying-parties` writes. Paths are relative to the file.
type File struct {
	PublicURL      string        `json:"public_url"`
	RelyingParties []PartyFile   `json:"relying_parties"`
	Credentials    []Credential  `json:"credentials"`
	IssuerAnchors  []AnchorsFile `json:"issuer_anchors"`
}

// PartyFile is one relying party: its access certificate (WRPAC) and key,
// and its registration certificate (WRPRC).
type PartyFile struct {
	ID                          string `json:"id"`
	Name                        string `json:"name"`
	KeyFile                     string `json:"key_file"`
	ChainFile                   string `json:"chain_file"`
	RegistrationCertificateFile string `json:"registration_certificate_file"`
}

// Credential is an attestation type the relying parties are registered
// for, with the claims they may ask for.
type Credential struct {
	// ID is the credential configuration of the issuer.
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	VCT    string     `json:"vct"`
	Claims [][]string `json:"claims"`
}

// AnchorsFile names the roots of the issuers whose credentials are
// accepted.
type AnchorsFile struct {
	Name             string `json:"name"`
	CertificatesFile string `json:"certificates_file"`
}

// Config is the loaded configuration.
type Config struct {
	PublicURL   string
	Parties     []Party
	Credentials []Credential
	Anchors     []x5c.Anchors
}

// Party is a relying party ready to sign requests.
type Party struct {
	ID           string
	Name         string
	Signer       *oid4vp.RequestSigner
	Registration string
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Load reads and checks the configuration file.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var f File
	if err := json.Unmarshal(raw, &f); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	u, err := url.Parse(f.PublicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" {
		return Config{}, fmt.Errorf("public_url %q must be an origin", f.PublicURL)
	}
	cfg := Config{PublicURL: f.PublicURL, Credentials: f.Credentials}
	if len(f.RelyingParties) == 0 || len(f.Credentials) == 0 || len(f.IssuerAnchors) == 0 {
		return Config{}, errors.New("relying_parties, credentials and issuer_anchors are required")
	}
	seen := map[string]bool{}
	for _, p := range f.RelyingParties {
		if !idPattern.MatchString(p.ID) || seen[p.ID] || p.Name == "" {
			return Config{}, fmt.Errorf("relying party %q needs a unique id and a name", p.ID)
		}
		seen[p.ID] = true
		key, err := readKey(resolve(p.KeyFile))
		if err != nil {
			return Config{}, fmt.Errorf("relying party %s: %w", p.ID, err)
		}
		chain, err := readCertificates(resolve(p.ChainFile))
		if err != nil {
			return Config{}, fmt.Errorf("relying party %s: %w", p.ID, err)
		}
		signer, err := oid4vp.NewRequestSigner(key, chain)
		if err != nil {
			return Config{}, fmt.Errorf("relying party %s: %w", p.ID, err)
		}
		reg, err := os.ReadFile(resolve(p.RegistrationCertificateFile))
		if err != nil {
			return Config{}, fmt.Errorf("relying party %s: %w", p.ID, err)
		}
		cfg.Parties = append(cfg.Parties, Party{ID: p.ID, Name: p.Name, Signer: signer, Registration: string(reg)})
	}
	seen = map[string]bool{}
	for _, c := range f.Credentials {
		if !idPattern.MatchString(c.ID) || seen[c.ID] || c.VCT == "" || len(c.Claims) == 0 {
			return Config{}, fmt.Errorf("credential %q needs a unique id, a vct and claims", c.ID)
		}
		seen[c.ID] = true
	}
	for _, a := range f.IssuerAnchors {
		roots, err := readCertificates(resolve(a.CertificatesFile))
		if err != nil {
			return Config{}, fmt.Errorf("issuer anchors %s: %w", a.Name, err)
		}
		cfg.Anchors = append(cfg.Anchors, x5c.NewAnchors(a.Name, roots...))
	}
	return cfg, nil
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
