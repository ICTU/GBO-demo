// Package lote consumes the GBO demo's signed, role-specific ETSI JSON lists.
// It is an HTTP adapter, not a source of implicit trust: its signing root is
// provisioned independently of the endpoint and the certificates in its JWS.
package lote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"gbo-demo/oid4vc-kit/x5c"
	"github.com/go-jose/go-jose/v4"
)

const prefix = "http://uri.etsi.org/19602/"
const RefreshInterval = time.Hour

// File configures an explicit LoTE trust source. It cannot be combined with
// static PID anchors. The signer root file contains public CA certificates.
type File struct {
	URL            string `json:"url"`
	SignerRootFile string `json:"signer_root_file"`
}

// Open reads only the independently provisioned public signing roots; the
// first trust check fetches the list, so an outage does not stop other flows.
func (f File) Open(resolve func(string) string) (*Client, error) {
	raw, err := os.ReadFile(resolve(f.SignerRootFile))
	if err != nil {
		return nil, fmt.Errorf("LoTE signer root: %w", err)
	}
	var roots []*x509.Certificate
	for len(bytes.TrimSpace(raw)) > 0 {
		b, rest := pem.Decode(raw)
		if b == nil {
			return nil, errors.New("LoTE signer root: invalid PEM")
		}
		raw = rest
		if b.Type != "CERTIFICATE" {
			return nil, errors.New("LoTE signer root: expected certificate")
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, err
		}
		roots = append(roots, c)
	}
	return New(f.URL, "PID", roots, nil, nil)
}

// Client serializes refreshes and retains the last verified list in memory.
// Checks refresh on first use and at least hourly while the source is used.
// Failed refreshes retry after a minute; an expired cache is never returned.
type Client struct {
	url, role string
	roots     []x5c.Anchors
	http      *http.Client
	now       func() time.Time
	mu        sync.Mutex
	cached    snapshot
	retry     time.Time
}
type snapshot struct {
	certs    []*x509.Certificate
	trust    x5c.Trust
	next     time.Time
	sequence uint64
	digest   [32]byte
}

func New(endpoint, role string, roots []*x509.Certificate, base *http.Client, now func() time.Time) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("LoTE URL must use HTTPS without credentials or fragment")
	}
	switch role {
	case "PID", "PubEAA", "WRPAC", "WRPRC":
	default:
		return nil, errors.New("unsupported LoTE role")
	}
	if len(roots) == 0 {
		return nil, errors.New("independently provisioned LoTE signing roots are required")
	}
	for _, r := range roots {
		if r == nil || !r.IsCA || !r.BasicConstraintsValid {
			return nil, errors.New("LoTE signing root must be a CA")
		}
	}
	if base == nil {
		base = &http.Client{Timeout: 10 * time.Second}
	}
	h := *base
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if h.Timeout == 0 {
		h.Timeout = 10 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	return &Client{url: endpoint, role: role, roots: []x5c.Anchors{x5c.NewAnchors("lote-signer", roots...)}, http: &h, now: now}, nil
}

// Load implements the consuming services' PIDTrust ports. Callers must not
// mutate returned pools. A new verifier is built for each response.
func (c *Client) Load(ctx context.Context) (x5c.Trust, error) {
	s, err := c.load(ctx)
	return s.trust, err
}

// Certificates exports the same verified snapshot for startup-only consumers.
func (c *Client) Certificates(ctx context.Context) ([]*x509.Certificate, error) {
	s, err := c.load(ctx)
	return append([]*x509.Certificate(nil), s.certs...), err
}

func (c *Client) load(ctx context.Context) (snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	var refreshErr error
	if !now.Before(c.retry) {
		s, err := c.fetch(ctx, now)
		if err == nil && c.cached.sequence > 0 {
			if s.sequence < c.cached.sequence || (s.sequence == c.cached.sequence && s.digest != c.cached.digest) {
				err = errors.New("LoTE sequence rollback or conflicting publication")
			}
		}
		if err == nil {
			c.cached = s
			c.retry = now.Add(RefreshInterval)
			if s.next.Before(c.retry) {
				c.retry = s.next
			}
		} else {
			refreshErr = err
			c.retry = now.Add(time.Minute)
		}
	}
	if len(c.cached.certs) > 0 && c.now().Before(c.cached.next) {
		return c.cached, nil
	}
	if refreshErr == nil {
		refreshErr = errors.New("no unexpired verified list in cache")
	}
	return snapshot{}, fmt.Errorf("PID trust temporarily unavailable: %w", refreshErr)
}

func (c *Client) fetch(ctx context.Context, now time.Time) (snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return snapshot{}, err
	}
	// Revalidate intermediary caches so their TTL cannot add another hour.
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.http.Do(req)
	if err != nil {
		return snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snapshot{}, fmt.Errorf("LoTE HTTP status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return snapshot{}, err
	}
	if len(raw) > 4<<20 {
		return snapshot{}, errors.New("LoTE exceeds 4 MiB")
	}
	jws, err := jose.ParseSigned(string(raw), []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return snapshot{}, err
	}
	if len(jws.Signatures) != 1 {
		return snapshot{}, errors.New("LoTE requires one signature")
	}
	chains, err := jws.Signatures[0].Header.Certificates(x509.VerifyOptions{
		Roots: c.roots[0].Roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	if err != nil {
		return snapshot{}, err
	}
	if len(chains) == 0 || len(chains[0]) == 0 {
		return snapshot{}, errors.New("LoTE signer has no chain to the independent pin")
	}
	leaf := chains[0][0]
	if leaf.IsCA || x5c.SelfSigned(leaf) || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return snapshot{}, errors.New("invalid LoTE signing certificate")
	}
	payload, err := jws.Verify(leaf.PublicKey)
	if err != nil {
		return snapshot{}, err
	}
	var doc struct {
		LoTE struct {
			Info struct {
				Type     string    `json:"LoTEType"`
				Version  int       `json:"LoTEVersionIdentifier"`
				Sequence uint64    `json:"LoTESequenceNumber"`
				Issue    time.Time `json:"ListIssueDateTime"`
				Next     time.Time `json:"NextUpdate"`
			} `json:"ListAndSchemeInformation"`
			Entities []struct {
				Services []struct {
					Info struct {
						Type     string `json:"ServiceTypeIdentifier"`
						Identity struct {
							Certificates []struct {
								Value string `json:"val"`
							} `json:"X509Certificates"`
						} `json:"ServiceDigitalIdentity"`
					} `json:"ServiceInformation"`
				} `json:"TrustedEntityServices"`
			} `json:"TrustedEntitiesList"`
		}
	}
	if err = json.Unmarshal(payload, &doc); err != nil {
		return snapshot{}, err
	}
	info := doc.LoTE.Info
	if info.Type != prefix+"LoTEType/EU"+c.role+"ProvidersList" || info.Version != 1 || info.Sequence == 0 {
		return snapshot{}, errors.New("wrong LoTE role, version or sequence")
	}
	if info.Issue.IsZero() || info.Issue.After(now) || !info.Next.After(info.Issue) || !now.Before(info.Next) {
		return snapshot{}, errors.New("LoTE validity interval is not current")
	}
	s := snapshot{sequence: info.Sequence, next: info.Next, digest: sha256.Sum256(payload)}
	// A cached list cannot outlive any certificate in its verified chain.
	for _, cert := range chains[0] {
		if cert.NotAfter.Before(s.next) {
			s.next = cert.NotAfter
		}
	}
	seen := map[[32]byte]bool{}
	for _, entity := range doc.LoTE.Entities {
		for _, service := range entity.Services {
			if service.Info.Type != prefix+"SvcType/"+c.role+"/Issuance" {
				continue
			}
			for _, id := range service.Info.Identity.Certificates {
				der, err := base64.StdEncoding.DecodeString(id.Value)
				if err != nil {
					return snapshot{}, err
				}
				cert, err := x509.ParseCertificate(der)
				if err != nil {
					return snapshot{}, err
				}
				if !cert.IsCA || !cert.BasicConstraintsValid {
					return snapshot{}, errors.New("LoTE issuance anchor is not a CA")
				}
				// KeyUsage is optional (the NL pre-production CA omits it).
				// If present, it must permit certificate signing (RFC 5280 4.2.1.3).
				for _, ext := range cert.Extensions {
					if ext.Id.String() == "2.5.29.15" && cert.KeyUsage&x509.KeyUsageCertSign == 0 {
						return snapshot{}, errors.New("LoTE issuance CA key usage forbids certificate signing")
					}
				}
				fp := sha256.Sum256(der)
				if seen[fp] {
					continue
				}
				seen[fp] = true
				s.certs = append(s.certs, cert)
				s.trust.Anchors = append(s.trust.Anchors, x5c.NewAnchors(fmt.Sprintf("lote-%x", fp[:8]), cert))
				if len(cert.SubjectKeyId) > 0 {
					s.trust.Authorities = append(s.trust.Authorities, base64.RawURLEncoding.EncodeToString(cert.SubjectKeyId))
				}
			}
		}
	}
	if len(s.certs) == 0 {
		return snapshot{}, errors.New("LoTE has no issuance anchors for the required role")
	}
	return s, nil
}
