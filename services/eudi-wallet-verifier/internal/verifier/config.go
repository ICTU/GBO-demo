package verifier

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
	"strings"
	"time"

	"gbo-demo/oid4vc-kit/oid4vp"
	"gbo-demo/oid4vc-kit/x5c"
)

// File is the configuration file of the verifier. Relative paths are
// resolved against the file's directory.
type File struct {
	// PublicURL is where wallets reach the wallet API; request_uri and
	// response_uri are below it.
	PublicURL string `json:"public_url"`
	// APIKeyFile holds the key of the internal API.
	APIKeyFile string `json:"api_key_file"`
	// SessionTTL is how long a session waits for the wallet; 5m when empty.
	SessionTTL string `json:"session_ttl"`
	// PIDAnchors are the roots of the PID issuers the verifier trusts, one
	// anchor set per entry.
	PIDAnchors []AnchorFile `json:"pid_anchors"`
	// PIDTrustedAuthorities are base64url KeyIdentifiers the verifier puts
	// in every query as an aki trusted_authorities query (HAIP 1.0 section
	// 5).
	PIDTrustedAuthorities []string               `json:"pid_trusted_authorities"`
	Usecases              map[string]UsecaseFile `json:"usecases"`
}

// AnchorFile is a named anchor set: the certificates of a PEM file.
type AnchorFile struct {
	Name      string `json:"name"`
	RootsFile string `json:"roots_file"`
}

// UsecaseFile is a relying party of the verifier: its access certificate
// (WRPAC), its registration certificate (WRPRC), and the PID claims that
// registration allows.
type UsecaseFile struct {
	KeyFile                     string   `json:"key_file"`
	ChainFile                   string   `json:"chain_file"`
	RegistrationCertificateFile string   `json:"registration_certificate_file"`
	PIDType                     string   `json:"pid_type"`
	Claims                      []string `json:"claims"`
	// ReturnURL is when a session gets a return URL, as in nl-wallet's
	// session_type_return_url: "same_device" or "neither".
	ReturnURL string `json:"return_url"`
}

// Config is the loaded configuration.
type Config struct {
	PublicURL             string
	APIKey                string
	SessionTTL            time.Duration
	PIDAnchors            []x5c.Anchors
	PIDTrustedAuthorities []string
	Usecases              map[string]Usecase
}

// Usecase is a loaded usecase.
type Usecase struct {
	Signer                  *oid4vp.RequestSigner
	RegistrationCertificate string
	PIDType                 string
	Claims                  []string
	SameDeviceReturn        bool
}

// DefaultSessionTTL is how long a session waits when the file sets none:
// as long as the authorization server keeps its transaction.
const DefaultSessionTTL = 5 * time.Minute

// Load reads and checks the configuration file at path.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var f File
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	u, err := url.Parse(f.PublicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Config{}, fmt.Errorf("public_url %q is not an http(s) URL", f.PublicURL)
	}
	cfg := Config{PublicURL: strings.TrimRight(f.PublicURL, "/"), SessionTTL: DefaultSessionTTL,
		PIDTrustedAuthorities: f.PIDTrustedAuthorities, Usecases: map[string]Usecase{}}
	if f.SessionTTL != "" {
		if cfg.SessionTTL, err = time.ParseDuration(f.SessionTTL); err != nil || cfg.SessionTTL <= 0 {
			return Config{}, fmt.Errorf("session_ttl %q is not a positive duration", f.SessionTTL)
		}
	}
	key, err := os.ReadFile(resolve(f.APIKeyFile))
	if err != nil {
		return Config{}, fmt.Errorf("api_key_file: %w", err)
	}
	if cfg.APIKey = strings.TrimSpace(string(key)); cfg.APIKey == "" {
		return Config{}, errors.New("api_key_file: the API key is empty")
	}
	if len(f.PIDAnchors) == 0 {
		return Config{}, errors.New("pid_anchors: at least one anchor set is required")
	}
	for _, a := range f.PIDAnchors {
		roots, err := readCertificates(resolve(a.RootsFile))
		if err != nil {
			return Config{}, fmt.Errorf("pid_anchors %q: %w", a.Name, err)
		}
		cfg.PIDAnchors = append(cfg.PIDAnchors, x5c.NewAnchors(a.Name, roots...))
	}
	if len(f.Usecases) == 0 {
		return Config{}, errors.New("usecases: at least one usecase is required")
	}
	for name, uf := range f.Usecases {
		uc, err := loadUsecase(uf, resolve)
		if err != nil {
			return Config{}, fmt.Errorf("usecase %q: %w", name, err)
		}
		cfg.Usecases[name] = uc
	}
	return cfg, nil
}

func loadUsecase(f UsecaseFile, resolve func(string) string) (Usecase, error) {
	key, err := readKey(resolve(f.KeyFile))
	if err != nil {
		return Usecase{}, err
	}
	chain, err := readCertificates(resolve(f.ChainFile))
	if err != nil {
		return Usecase{}, err
	}
	signer, err := oid4vp.NewRequestSigner(key, chain)
	if err != nil {
		return Usecase{}, err
	}
	rc, err := os.ReadFile(resolve(f.RegistrationCertificateFile))
	if err != nil {
		return Usecase{}, err
	}
	if f.PIDType == "" || len(f.Claims) == 0 {
		return Usecase{}, errors.New("pid_type and claims are required")
	}
	uc := Usecase{Signer: signer, RegistrationCertificate: strings.TrimSpace(string(rc)), PIDType: f.PIDType, Claims: f.Claims}
	switch f.ReturnURL {
	case "same_device":
		uc.SameDeviceReturn = true
	case "", "neither":
	default:
		return Usecase{}, fmt.Errorf("return_url %q is not same_device or neither", f.ReturnURL)
	}
	return uc, nil
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
