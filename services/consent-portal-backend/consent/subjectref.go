package consent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// SubjectRefs derives the portal's own reference to a citizen: an HMAC of the
// BSN under a secret key of the portal. The same BSN always gives the same
// reference, so the portal finds a citizen's consents, and the polymorphic
// values from their first activation, at every login. It asks nobody for it.
//
// The key is the whole protection. There are about a billion BSNs, so a
// reference made without a secret key could be recomputed by anyone with a
// list of them.
type SubjectRefs struct {
	Key []byte
	// Version names the key. It is part of every reference, so that a record
	// says which key made it: a new key gives every citizen a new reference,
	// and the portal can move a record to it only when the BSN comes in again.
	Version string
}

// subjectRefDomain keeps these references apart from any other HMAC made
// with the same key.
const subjectRefDomain = "gbo-consent-portal-subject:"

var (
	// ErrNoSubjectKey means the portal has no key to derive references with.
	ErrNoSubjectKey = errors.New("no subject reference key configured")
	// ErrInvalidBSN means the value is not a BSN: one to nine digits.
	ErrInvalidBSN = errors.New("not a BSN")
)

// For derives the reference for a BSN. A BSN with or without its leading zero
// is the same BSN and gives the same reference.
func (s SubjectRefs) For(citizen BSN) (SubjectRef, error) {
	if len(s.Key) == 0 {
		return "", ErrNoSubjectKey
	}
	bsn := strings.TrimSpace(string(citizen))
	if len(bsn) == 0 || len(bsn) > 9 || strings.Trim(bsn, "0123456789") != "" {
		return "", ErrInvalidBSN
	}
	bsn = strings.Repeat("0", 9-len(bsn)) + bsn

	mac := hmac.New(sha256.New, s.Key)
	_, _ = mac.Write([]byte(subjectRefDomain + bsn))
	return SubjectRef("SR" + s.Version + "-" + hex.EncodeToString(mac.Sum(nil))), nil
}
