// Package statuslist builds Token Status Lists
// (draft-ietf-oauth-status-list) with one bit per entry, 0 valid and 1
// revoked, and reads lists with 1, 2, 4 or 8 bits per entry.
package statuslist

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"gbo-demo/oid4vc-kit/x5c"
)

// Type is the typ header of a Status List Token in JWT format.
const Type = "statuslist+jwt"

// Encode returns the lst value for a list of size entries in which the
// given indices are revoked: the bits, least significant first within each
// byte, zlib-compressed and base64url encoded.
func Encode(size int, revoked []int) (string, error) {
	bits := make([]byte, (size+7)/8)
	for _, i := range revoked {
		if i < 0 || i >= size {
			return "", errors.New("statuslist: index out of range")
		}
		bits[i/8] |= 1 << (i % 8)
	}
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(bits); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// maxListSize bounds a decompressed list: 64 million one-bit entries.
const maxListSize = 8 << 20

// Decode returns the status bits of an lst value.
func Decode(lst string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(lst)
	if err != nil {
		return nil, err
	}
	r, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	bits, err := io.ReadAll(io.LimitReader(r, maxListSize+1))
	if err != nil {
		return nil, err
	}
	if len(bits) > maxListSize {
		return nil, errors.New("statuslist: the list is too large")
	}
	return bits, nil
}

// Revoked reports whether entry idx is set in the decoded bits.
func Revoked(bits []byte, idx int) bool {
	return idx >= 0 && idx/8 < len(bits) && bits[idx/8]&(1<<(idx%8)) != 0
}

// Token describes a Status List Token to sign.
type Token struct {
	// URI is where the token is served; it is the sub claim.
	URI      string
	IssuedAt time.Time
	Expires  time.Time
	// TTL tells relying parties how long they may cache the token.
	TTL     time.Duration
	Size    int
	Revoked []int
}

// Sign returns the token as a JWT signed by key, publishing x5c in the
// header.
func Sign(t Token, key crypto.Signer, x5c []string) (string, error) {
	lst, err := Encode(t.Size, t.Revoked)
	if err != nil {
		return "", err
	}
	opts := (&jose.SignerOptions{}).WithType(Type).WithHeader("x5c", x5c)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key}, opts)
	if err != nil {
		return "", err
	}
	claims := map[string]any{
		"sub":         t.URI,
		"iat":         t.IssuedAt.Unix(),
		"exp":         t.Expires.Unix(),
		"ttl":         int64(t.TTL / time.Second),
		"status_list": map[string]any{"bits": 1, "lst": lst},
	}
	return jwt.Signed(signer).Claims(claims).Serialize()
}

// MediaType is the media type of a Status List Token in JWT format.
const MediaType = "application/statuslist+jwt"

// maxTokenSize bounds a fetched Status List Token.
const maxTokenSize = 1 << 20

// Fetch retrieves the Status List Token at uri (section 8.1).
func Fetch(ctx context.Context, client *http.Client, uri string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", MediaType)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("statuslist: %s answered %d", uri, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenSize+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxTokenSize {
		return "", errors.New("statuslist: token too large")
	}
	return strings.TrimSpace(string(body)), nil
}

// ExpiryLeeway is allowed on the exp of a Status List Token.
const ExpiryLeeway = time.Minute

// SignerEKU is the extended key usage of a Status List Token signer
// (id-kp-oauthStatusSigning, draft-ietf-oauth-status-list section 10.2).
var SignerEKU = asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 3, 127}

// List is a verified Status List Token.
type List struct {
	// Signer is the certificate that signed the token.
	Signer *x509.Certificate

	bits int
	lst  []byte
}

// Verify checks a Status List Token in JWT format that was fetched from
// uri (section 8.3): the typ, the x5c chain against anchors with SignerEKU
// on the leaf, the signature,
// sub equal to uri, iat present and exp, when present, not passed by more
// than ExpiryLeeway, as the NL Wallet allows. The
// list may use 1, 2, 4 or 8 bits per entry.
func Verify(token, uri string, anchors []x5c.Anchors, now time.Time) (List, error) {
	tok, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return List{}, fmt.Errorf("statuslist: %w", err)
	}
	h := tok.Headers[0]
	if typ, _ := h.ExtraHeaders[jose.HeaderType].(string); typ != Type {
		return List{}, fmt.Errorf("statuslist: typ must be %q", Type)
	}
	leaf, _, err := x5c.Verify(h, anchors, now, SignerEKU)
	if err != nil {
		return List{}, err
	}
	var c struct {
		Sub        string `json:"sub"`
		IssuedAt   *int64 `json:"iat"`
		Expires    *int64 `json:"exp"`
		StatusList struct {
			Bits int    `json:"bits"`
			Lst  string `json:"lst"`
		} `json:"status_list"`
	}
	if err := tok.Claims(leaf.PublicKey, &c); err != nil {
		return List{}, errors.New("statuslist: signature does not verify")
	}
	if c.Sub != uri {
		return List{}, errors.New("statuslist: sub is not the uri it was fetched from")
	}
	if c.IssuedAt == nil {
		return List{}, errors.New("statuslist: iat is missing")
	}
	if c.Expires != nil {
		if now.After(time.Unix(*c.Expires, 0).Add(ExpiryLeeway)) {
			return List{}, errors.New("statuslist: token has expired")
		}
	}
	switch c.StatusList.Bits {
	case 1, 2, 4, 8:
	default:
		return List{}, errors.New("statuslist: bits must be 1, 2, 4 or 8")
	}
	lst, err := Decode(c.StatusList.Lst)
	if err != nil {
		return List{}, errors.New("statuslist: lst does not decode")
	}
	return List{Signer: leaf, bits: c.StatusList.Bits, lst: lst}, nil
}

// Status returns the status of entry idx: 0 is valid (section 7). An
// index outside the list is an error.
func (l List) Status(idx int) (byte, error) {
	if l.bits == 0 || idx < 0 || idx >= len(l.lst)*8/l.bits {
		return 0, errors.New("statuslist: index outside the list")
	}
	bit := idx * l.bits
	return (l.lst[bit/8] >> (bit % 8)) & (1<<l.bits - 1), nil
}
