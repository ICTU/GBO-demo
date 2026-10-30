// Package sdjwtvc issues and parses SD-JWT-based Verifiable Credentials
// (SD-JWT, RFC 9901; SD-JWT VC, draft-ietf-oauth-sd-jwt-vc) with typ
// dc+sd-jwt, as HAIP 1.0 section 6.1 profiles them.
package sdjwtvc

import (
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Type is the typ header of an SD-JWT VC.
const Type = "dc+sd-jwt"

// reserved are the top-level claims that SD-JWT VC forbids to be
// selectively disclosable (draft-ietf-oauth-sd-jwt-vc-19 section 2.2.2.3),
// plus the names SD-JWT itself uses.
var reserved = []string{"iss", "nbf", "exp", "cnf", "vct", "vct#integrity", "aka_vcts", "status", "iat", "_sd", "_sd_alg", "..."}

// Reserved reports whether name may not be an attribute at the top level
// of an SD-JWT VC.
func Reserved(name string) bool {
	return slices.Contains(reserved, name)
}

// StatusReference points at an entry in a Token Status List.
type StatusReference struct {
	Index int    `json:"idx"`
	URI   string `json:"uri"`
}

// Credential describes one SD-JWT VC to issue.
type Credential struct {
	VCT       string
	IssuedAt  time.Time
	NotBefore time.Time
	Expires   time.Time
	// HolderKey is the public key the credential is bound to (cnf.jwk).
	HolderKey *jose.JSONWebKey
	Status    *StatusReference
	// Claims are the attributes. Every property of every object is made
	// selectively disclosable; array elements are disclosed as a whole.
	Claims map[string]any
}

// Signer signs SD-JWT VCs with an issuer key and certificate chain.
type Signer struct {
	signer jose.Signer
}

// NewSigner returns a signer for key, publishing x5c (base64 DER, leaf
// first) in the header.
func NewSigner(key crypto.Signer, x5c []string) (*Signer, error) {
	opts := (&jose.SignerOptions{}).WithType(Type).WithHeader("x5c", x5c)
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
	if err != nil {
		return nil, err
	}
	return &Signer{signer: s}, nil
}

// Issue returns the credential in combined format: the issuer-signed JWT
// followed by all disclosures, each terminated by '~'.
func (s *Signer) Issue(c Credential) (string, error) {
	if c.VCT == "" || c.HolderKey == nil {
		return "", errors.New("sdjwtvc: vct and holder key are required")
	}
	var disclosures []string
	payload, err := conceal(c.Claims, &disclosures, true)
	if err != nil {
		return "", err
	}
	payload["_sd_alg"] = "sha-256"
	payload["vct"] = c.VCT
	payload["iat"] = c.IssuedAt.Unix()
	payload["nbf"] = c.NotBefore.Unix()
	payload["exp"] = c.Expires.Unix()
	payload["cnf"] = map[string]any{"jwk": c.HolderKey}
	if c.Status != nil {
		payload["status"] = map[string]any{"status_list": c.Status}
	}
	token, err := jwt.Signed(s.signer).Claims(payload).Serialize()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(token + "~")
	for _, d := range disclosures {
		b.WriteString(d + "~")
	}
	return b.String(), nil
}

// conceal turns every property of obj into a disclosure, recursing into
// nested objects, and returns the object with the _sd digests that
// replace them.
func conceal(obj map[string]any, disclosures *[]string, top bool) (map[string]any, error) {
	out := map[string]any{}
	var digests []string
	for _, name := range sortedKeys(obj) {
		if (top && Reserved(name)) || name == "_sd" || name == "..." {
			return nil, fmt.Errorf("sdjwtvc: claim %s may not be selectively disclosable", name)
		}
		value := obj[name]
		if nested, ok := value.(map[string]any); ok {
			v, err := conceal(nested, disclosures, false)
			if err != nil {
				return nil, err
			}
			value = v
		}
		d, err := disclosure(name, value)
		if err != nil {
			return nil, err
		}
		*disclosures = append(*disclosures, d)
		digests = append(digests, Digest(d))
	}
	if len(digests) > 0 {
		// Sorting hides the original claim order (RFC 9901 section 4.2.4.1).
		sort.Strings(digests)
		out["_sd"] = digests
	}
	return out, nil
}

func disclosure(name string, value any) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	raw, err := json.Marshal([]any{base64.RawURLEncoding.EncodeToString(salt), name, value})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Digest is the sha-256 digest of a disclosure as it appears in _sd.
func Digest(disclosure string) string {
	sum := sha256.Sum256([]byte(disclosure))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
