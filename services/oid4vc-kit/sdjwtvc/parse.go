package sdjwtvc

import (
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/jwttyp"
	"gbo-demo/oid4vc-kit/x5c"
)

// Parsed is a verified SD-JWT VC with its disclosures applied.
type Parsed struct {
	Issuer *x509.Certificate
	// Anchor names the anchor set that accepted the issuer's chain.
	Anchor string
	Header jose.Header
	// Claims hold the issuer-signed claims with every disclosed claim put
	// back in place; _sd and _sd_alg are removed.
	Claims map[string]any
	// Disclosed holds the claim path of every disclosure, so a verifier
	// can tell what the holder chose to reveal.
	Disclosed [][]string
}

// Parse verifies an SD-JWT VC in combined format without key binding: the
// typ, the x5c chain against anchors (with eku when given), the signature
// and every disclosure digest.
func Parse(combined string, anchors []x5c.Anchors, eku asn1.ObjectIdentifier, now time.Time) (Parsed, error) {
	parts := strings.Split(combined, "~")
	if len(parts) < 2 || parts[len(parts)-1] != "" {
		return Parsed{}, errors.New("sdjwtvc: not an SD-JWT without key binding")
	}
	tok, err := jwt.ParseSigned(parts[0], []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return Parsed{}, fmt.Errorf("sdjwtvc: %w", err)
	}
	h := tok.Headers[0]
	if !jwttyp.Is(h, Type) {
		return Parsed{}, fmt.Errorf("sdjwtvc: typ must be %q", Type)
	}
	leaf, anchor, err := x5c.Verify(h, anchors, now, eku)
	if err != nil {
		return Parsed{}, err
	}
	var payload map[string]any
	if err := tok.Claims(leaf.PublicKey, &payload); err != nil {
		return Parsed{}, errors.New("sdjwtvc: signature does not verify")
	}
	if alg, _ := payload["_sd_alg"].(string); alg != "sha-256" {
		return Parsed{}, errors.New("sdjwtvc: _sd_alg must be sha-256")
	}
	delete(payload, "_sd_alg")

	byDigest := map[string][]any{}
	for _, d := range parts[1 : len(parts)-1] {
		raw, err := base64.RawURLEncoding.DecodeString(d)
		if err != nil {
			return Parsed{}, errors.New("sdjwtvc: disclosure is not base64url")
		}
		var arr []any
		if err := json.Unmarshal(raw, &arr); err != nil || len(arr) != 3 {
			return Parsed{}, errors.New("sdjwtvc: only object property disclosures are supported")
		}
		digest := Digest(d)
		if _, dup := byDigest[digest]; dup {
			return Parsed{}, errors.New("sdjwtvc: duplicate disclosure")
		}
		byDigest[digest] = arr
	}
	var disclosed [][]string
	if err := reveal(payload, byDigest, map[string]bool{}, nil, &disclosed); err != nil {
		return Parsed{}, err
	}
	if len(byDigest) > 0 {
		return Parsed{}, errors.New("sdjwtvc: disclosure not referenced by any digest")
	}
	return Parsed{Issuer: leaf, Anchor: anchor, Header: h, Claims: payload, Disclosed: disclosed}, nil
}

// reveal replaces the _sd digests in obj, recursively, by the disclosed
// claims, removes each used disclosure from byDigest and records its path
// below path in disclosed. A digest that occurs twice is refused (RFC 9901
// section 7.1).
func reveal(obj map[string]any, byDigest map[string][]any, seen map[string]bool, path []string, disclosed *[][]string) error {
	if raw, ok := obj["_sd"]; ok {
		list, ok := raw.([]any)
		if !ok {
			return errors.New("sdjwtvc: _sd must be an array")
		}
		delete(obj, "_sd")
		for _, item := range list {
			digest, _ := item.(string)
			if seen[digest] {
				return errors.New("sdjwtvc: a digest occurs more than once")
			}
			seen[digest] = true
			arr, ok := byDigest[digest]
			if !ok {
				continue // a decoy or an undisclosed claim
			}
			delete(byDigest, digest)
			name, ok := arr[1].(string)
			if !ok {
				return errors.New("sdjwtvc: a disclosure's claim name must be a string")
			}
			if _, exists := obj[name]; exists || name == "_sd" || name == "..." {
				return fmt.Errorf("sdjwtvc: disclosure for %q conflicts with an existing claim", name)
			}
			obj[name] = arr[2]
			*disclosed = append(*disclosed, append(slices.Clone(path), name))
		}
	}
	for name, v := range obj {
		if nested, ok := v.(map[string]any); ok {
			if err := reveal(nested, byDigest, seen, append(slices.Clone(path), name), disclosed); err != nil {
				return err
			}
		}
	}
	return nil
}
