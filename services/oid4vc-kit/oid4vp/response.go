package oid4vp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/jwttyp"
	"gbo-demo/oid4vc-kit/sdjwtvc"
	"gbo-demo/oid4vc-kit/statuslist"
	"gbo-demo/oid4vc-kit/x5c"
)

// ErrStatusUnavailable means the credential's status list could not be
// fetched; ErrRevoked that the list does not mark the credential valid.
// Every other error means the response itself is not acceptable.
var (
	ErrStatusUnavailable = errors.New("oid4vp: the status list is unavailable")
	ErrRevoked           = errors.New("oid4vp: the status list does not mark the credential valid")
)

// StatusFetcher returns the Status List Token served at uri.
type StatusFetcher func(ctx context.Context, uri string) (string, error)

// Verifier checks responses against the issuers it trusts.
type Verifier struct {
	// Anchors are the roots of the credential issuers, for example the NL
	// pre-production PID issuer and the test PID. A credential's status
	// list must chain to the same anchor set as the credential.
	Anchors     []x5c.Anchors
	FetchStatus StatusFetcher
}

// Disclosure is a verified presentation.
type Disclosure struct {
	// Claims are the issuer-signed claims with the disclosed ones in place.
	Claims map[string]any
	// Anchor names the anchor set that accepted the credential.
	Anchor string
	Issuer *x509.Certificate
}

// VerifyResponse decrypts the response parameter of a direct_post.jwt
// response and verifies the one presentation the session's query asked
// for: the SD-JWT VC against the anchors, its key binding against the
// session, the disclosed claims against the query and the credential's
// status list. A disclosure of a claim the query did not ask for is
// refused, as is a credential without a status reference.
func (v Verifier) VerifyResponse(ctx context.Context, compact string, s Session, now time.Time) (Disclosure, error) {
	if err := v.validate(s); err != nil {
		return Disclosure{}, err
	}
	body, err := decrypt(compact, s.Key)
	if err != nil {
		return Disclosure{}, err
	}
	vpToken, state, err := parseResponse(body)
	if err != nil {
		return Disclosure{}, err
	}
	if state != s.State {
		return Disclosure{}, errors.New("oid4vp: state does not match the session")
	}
	q := s.Query.Credentials[0]
	presentations, ok := vpToken[q.ID]
	if !ok || len(vpToken) != 1 || len(presentations) != 1 {
		return Disclosure{}, errors.New("oid4vp: vp_token must hold exactly one presentation for the query")
	}
	sdjwt, kb, err := split(presentations[0])
	if err != nil {
		return Disclosure{}, err
	}
	parsed, err := sdjwtvc.Parse(sdjwt, v.Anchors, CredentialSignerEKU, now)
	if err != nil {
		return Disclosure{}, err
	}
	if err := checkCredential(parsed.Claims, q, now); err != nil {
		return Disclosure{}, err
	}
	if err := checkKeyBinding(kb, sdjwt, parsed.Claims, s, now); err != nil {
		return Disclosure{}, err
	}
	if err := checkDisclosed(parsed, q); err != nil {
		return Disclosure{}, err
	}
	if err := v.checkStatus(ctx, parsed, now); err != nil {
		return Disclosure{}, err
	}
	return Disclosure{Claims: parsed.Claims, Anchor: parsed.Anchor, Issuer: parsed.Issuer}, nil
}

// validate refuses a verifier or session the caller set up wrongly: anchor
// set names must be unique, since a status list is checked against the
// anchor set that accepted the credential.
func (v Verifier) validate(s Session) error {
	if v.FetchStatus == nil {
		return errors.New("oid4vp: the verifier has no status fetcher")
	}
	names := map[string]bool{}
	for _, a := range v.Anchors {
		if names[a.Name] {
			return fmt.Errorf("oid4vp: anchor set %q occurs twice", a.Name)
		}
		names[a.Name] = true
	}
	return s.validate()
}

// parseResponse reads vp_token and state from the decrypted response
// (section 8.1), with the member names matched exactly.
func parseResponse(body []byte) (map[string][]string, string, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(body, &members); err != nil {
		return nil, "", errors.New("oid4vp: the response is not a JSON object")
	}
	var vpToken map[string][]string
	if err := json.Unmarshal(members["vp_token"], &vpToken); err != nil {
		return nil, "", errors.New("oid4vp: vp_token must map query ids to presentations")
	}
	var state string
	if raw, ok := members["state"]; ok {
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, "", errors.New("oid4vp: state must be a string")
		}
	}
	return vpToken, state, nil
}

func decrypt(compact string, key ResponseKey) ([]byte, error) {
	obj, err := jose.ParseEncryptedCompact(compact, []jose.KeyAlgorithm{EncryptionAlgorithm}, EncryptionMethods)
	if err != nil {
		return nil, errors.New("oid4vp: the response is not a JWE encrypted with ECDH-ES and A128GCM or A256GCM")
	}
	if _, ok := obj.Header.ExtraHeaders["zip"]; ok {
		return nil, errors.New("oid4vp: compression is not supported")
	}
	if key.Key == nil || obj.Header.KeyID != key.KeyID {
		return nil, errors.New("oid4vp: the response is not encrypted to the session's key")
	}
	body, err := obj.Decrypt(key.Key)
	if err != nil {
		return nil, errors.New("oid4vp: the response does not decrypt")
	}
	return body, nil
}

// split separates an SD-JWT+KB into the SD-JWT, ending in '~', and the
// key binding JWT.
func split(presentation string) (string, string, error) {
	i := strings.LastIndex(presentation, "~")
	if i < 0 || i == len(presentation)-1 {
		return "", "", errors.New("oid4vp: the presentation has no key binding")
	}
	return presentation[:i+1], presentation[i+1:], nil
}

// checkCredential checks the type and, with ClockSkew, the validity
// period; exp is required.
func checkCredential(claims map[string]any, q CredentialQuery, now time.Time) error {
	vct, _ := claims["vct"].(string)
	if !slices.Contains(q.Meta.VCTValues, vct) {
		return fmt.Errorf("oid4vp: vct %q is not asked for", vct)
	}
	if exp, ok, err := numericDate(claims, "exp"); err != nil {
		return err
	} else if !ok {
		return errors.New("oid4vp: the credential has no exp")
	} else if now.After(exp.Add(ClockSkew)) {
		return errors.New("oid4vp: the credential has expired")
	}
	if nbf, ok, err := numericDate(claims, "nbf"); err != nil {
		return err
	} else if ok && now.Before(nbf.Add(-ClockSkew)) {
		return errors.New("oid4vp: the credential is not yet valid")
	}
	return nil
}

func numericDate(claims map[string]any, name string) (time.Time, bool, error) {
	raw, ok := claims[name]
	if !ok {
		return time.Time{}, false, nil
	}
	f, ok := raw.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return time.Time{}, false, fmt.Errorf("oid4vp: %s is not a number", name)
	}
	return time.Unix(int64(f), 0), true, nil
}

// checkKeyBinding verifies the KB-JWT (RFC 9901 section 4.3) with the
// credential's cnf key: aud is the client_id with its prefix, the nonce is
// the session's, iat is at most KeyBindingMaxAge old and sd_hash covers the
// SD-JWT as presented.
func checkKeyBinding(kb, sdjwt string, claims map[string]any, s Session, now time.Time) error {
	holder, err := holderKey(claims)
	if err != nil {
		return err
	}
	tok, err := jwt.ParseSigned(kb, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return errors.New("oid4vp: the key binding is not an ES256 JWT")
	}
	if !jwttyp.Is(tok.Headers[0], KeyBindingType) {
		return fmt.Errorf("oid4vp: the key binding typ must be %q", KeyBindingType)
	}
	var c struct {
		Aud    any    `json:"aud"`
		Nonce  string `json:"nonce"`
		Iat    *int64 `json:"iat"`
		SDHash string `json:"sd_hash"`
	}
	if err := tok.Claims(holder); err != nil {
		return errors.New("oid4vp: the key binding is not signed with the credential's key")
	}
	if err := tok.UnsafeClaimsWithoutVerification(&c); err != nil {
		return errors.New("oid4vp: the key binding claims are malformed")
	}
	if aud, _ := c.Aud.(string); aud != s.ClientID {
		return errors.New("oid4vp: the key binding aud is not the client_id")
	}
	if c.Nonce != s.Nonce {
		return errors.New("oid4vp: the key binding nonce is not the session's")
	}
	if c.Iat == nil {
		return errors.New("oid4vp: the key binding has no iat")
	}
	iat := time.Unix(*c.Iat, 0)
	if iat.After(now.Add(ClockSkew)) || iat.Before(now.Add(-KeyBindingMaxAge-ClockSkew)) {
		return errors.New("oid4vp: the key binding iat is outside the allowed window")
	}
	sum := sha256.Sum256([]byte(sdjwt))
	if c.SDHash != base64.RawURLEncoding.EncodeToString(sum[:]) {
		return errors.New("oid4vp: the key binding sd_hash does not match the presentation")
	}
	return nil
}

func holderKey(claims map[string]any) (*ecdsa.PublicKey, error) {
	cnf, _ := claims["cnf"].(map[string]any)
	raw, err := json.Marshal(cnf["jwk"])
	if err != nil || cnf["jwk"] == nil {
		return nil, errors.New("oid4vp: the credential has no cnf.jwk")
	}
	var k jose.JSONWebKey
	if err := json.Unmarshal(raw, &k); err != nil || !k.IsPublic() {
		return nil, errors.New("oid4vp: cnf.jwk is not a public key")
	}
	pub, ok := k.Key.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("oid4vp: cnf.jwk is not a P-256 key")
	}
	return pub, nil
}

// checkDisclosed requires every requested claim to be present and every
// disclosure to lie on the path of a requested claim: a parent of it, the
// claim itself or a part of it.
func checkDisclosed(p sdjwtvc.Parsed, q CredentialQuery) error {
	for _, cl := range q.Claims {
		if !present(p.Claims, cl.Path) {
			return fmt.Errorf("oid4vp: claim %v is not disclosed", cl.Path)
		}
	}
	for _, d := range p.Disclosed {
		if !slices.ContainsFunc(q.Claims, func(cl ClaimQuery) bool { return prefix(d, cl.Path) || prefix(cl.Path, d) }) {
			return fmt.Errorf("oid4vp: claim %v is disclosed but not asked for", d)
		}
	}
	return nil
}

func present(obj map[string]any, path []string) bool {
	for i, name := range path {
		v, ok := obj[name]
		if !ok || v == nil {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		if obj, ok = v.(map[string]any); !ok {
			return false
		}
	}
	return false
}

func prefix(p, of []string) bool {
	return len(p) <= len(of) && slices.Equal(p, of[:len(p)])
}

// checkStatus fetches the credential's status list, which must chain to
// the anchor set that accepted the credential and be signed under the same
// distinguished name as the credential, as the NL Wallet requires, and
// requires entry idx to be valid.
func (v Verifier) checkStatus(ctx context.Context, p sdjwtvc.Parsed, now time.Time) error {
	anchor := p.Anchor
	status, _ := p.Claims["status"].(map[string]any)
	ref, _ := status["status_list"].(map[string]any)
	idx, okIdx := ref["idx"].(float64)
	uri, _ := ref["uri"].(string)
	if !okIdx || uri == "" || idx < 0 || idx != math.Trunc(idx) || idx > math.MaxInt32 {
		return errors.New("oid4vp: the credential has no status list reference")
	}
	i := slices.IndexFunc(v.Anchors, func(a x5c.Anchors) bool { return a.Name == anchor })
	if i < 0 {
		return errors.New("oid4vp: the credential's anchor set is unknown")
	}
	token, err := v.FetchStatus(ctx, uri)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStatusUnavailable, err)
	}
	list, err := statuslist.Verify(token, uri, v.Anchors[i:i+1], now)
	if err != nil {
		return err
	}
	if list.Signer.Subject.ToRDNSequence().String() != p.Issuer.Subject.ToRDNSequence().String() {
		return errors.New("oid4vp: the status list is not signed under the credential signer's name")
	}
	value, err := list.Status(int(idx))
	if err != nil {
		return err
	}
	if value != 0 {
		return ErrRevoked
	}
	return nil
}
