// Package statuslist builds and reads Token Status Lists
// (draft-ietf-oauth-status-list) with one bit per entry: 0 is valid,
// 1 is revoked.
package statuslist

import (
	"bytes"
	"compress/zlib"
	"crypto"
	"encoding/base64"
	"errors"
	"io"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
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
	return io.ReadAll(r)
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
