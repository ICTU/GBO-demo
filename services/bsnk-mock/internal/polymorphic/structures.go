// Package polymorphic is the core of the BSNk mock. It issues the values BSNk
// issues and reads them the way a receiving party does, following BSNk's
// rules and naming but without its cryptography.
//
// A value has the fields of the real structure, under the real names. The
// real one is binary and carries three curve points; this one is JSON and
// carries the BSN or the pseudonym in plain sight. The transport form is the
// base64 of that JSON, so anyone can decode a value and read it, the way a
// test JWT carries a readable subject. Nothing here protects anything.
//
// The package has no transport or storage dependencies. The HTTP handlers in
// package main are its only adapter.
package polymorphic

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Notation identifiers of the structures, as the real ones carry them.
const (
	oidPolymorphicIdentity        = "2.16.528.1.1003.10.1.1.1"
	oidPolymorphicPseudonym       = "2.16.528.1.1003.10.1.1.2"
	oidSignedPolymorphicIdentity  = "2.16.528.1.1003.10.1.1.3.2"
	oidSignedPolymorphicPseudonym = "2.16.528.1.1003.10.1.1.4.2"
	oidEncryptedIdentity          = "2.16.528.1.1003.10.1.2.1"
	oidEncryptedPseudonym         = "2.16.528.1.1003.10.1.2.2"
	oidSignedEncryptedIdentity    = "2.16.528.1.1003.10.1.2.7.2"
	oidSignedEncryptedPseudonym   = "2.16.528.1.1003.10.1.2.8.2"
	oidDecryptedPseudonym         = "2.16.528.1.1003.10.1.3.2"

	oidECDSA  = "1.2.840.10045.4.3.3"
	oidECSDSA = "0.4.0.127.0.7.1.1.4.4.3"
)

const (
	// SchemeVersion and SchemeKeySetVersion are fixed: the mock has one
	// scheme and one set of scheme keys.
	SchemeVersion       = "1"
	SchemeKeySetVersion = "1"
	signingKeyVersion   = "1"

	// Creator is the OIN the mock puts in every value as its creator. It is
	// not the OIN of any party.
	Creator = "00000000000000000000"

	// pseudonymTypeBSN says a pseudonym is derived from a BSN.
	pseudonymTypeBSN = "B"

	// nonceKey is the extra element a relying party's nonce is signed in
	// under.
	nonceKey = "RP:Nonce"

	// stableIssuanceDate and stableAuditElement are what a value carries
	// when randomisation is off, so that it does not change from one run or
	// one month to the next.
	stableIssuanceDate = "20260101"
	stableAuditElement = "AAAAAAAAAAAAAAAAAAAAAA=="
)

// The scheme keys. With the real BSNk these are public keys that verify the
// signature on a value. Here each is the secret of an HMAC, published like a
// public key: it tells a value the mock issued from one that was altered or
// made up, and that is all it does.
var (
	polymorphicSigningKey = []byte("bsnk-mock scheme key U")
	identitySchemeKey     = []byte("bsnk-mock scheme key IP_P")
	pseudonymSchemeKey    = []byte("bsnk-mock scheme key PP_P")
)

// Scheme key types, as they appear in a scheme key URN.
const (
	schemeKeyIdentity  = "IP_P"
	schemeKeyPseudonym = "PP_P"
)

// SchemeKeys returns the scheme keys a receiving party needs to verify its
// values, keyed by URN.
func SchemeKeys() map[string]string {
	urn := func(keyType string) string {
		return "urn:nl-gdi-eid:1.0:pp-key:mock:" + SchemeKeySetVersion + ":" + keyType + ":" + SchemeKeySetVersion
	}
	return map[string]string{
		urn(schemeKeyIdentity):  base64.StdEncoding.EncodeToString(identitySchemeKey),
		urn(schemeKeyPseudonym): base64.StdEncoding.EncodeToString(pseudonymSchemeKey),
	}
}

// SignatureValue is the signature on a structure.
type SignatureValue struct {
	SignatureType string `json:"signatureType"`
	R             string `json:"r"`
	S             string `json:"s"`
}

// ExtraElement is a key and value signed into an encrypted structure.
type ExtraElement struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// PolymorphicIdentity leads to the BSN, for its recipient only.
type PolymorphicIdentity struct {
	NotationIdentifier     string `json:"notationIdentifier"`
	SchemeVersion          string `json:"schemeVersion"`
	SchemeKeySetVersion    string `json:"schemeKeySetVersion"`
	Creator                string `json:"creator"`
	Recipient              string `json:"recipient"`
	RecipientKeySetVersion string `json:"recipientKeySetVersion"`
	// IdentityValue is the BSN. The real structure has curve points here.
	IdentityValue string `json:"identityValue"`
}

// PolymorphicPseudonym leads to a pseudonym, for its recipient only.
type PolymorphicPseudonym struct {
	NotationIdentifier     string `json:"notationIdentifier"`
	SchemeVersion          string `json:"schemeVersion"`
	SchemeKeySetVersion    string `json:"schemeKeySetVersion"`
	Creator                string `json:"creator"`
	Recipient              string `json:"recipient"`
	RecipientKeySetVersion string `json:"recipientKeySetVersion"`
	Type                   string `json:"type"`
	// PseudonymSeed is a one-way derivative of the BSN, so that a
	// polymorphic pseudonym cannot give the BSN back. The real structure has
	// curve points here.
	PseudonymSeed string `json:"pseudonymSeed"`
}

// SignedPI is the signed part of a SignedPolymorphicIdentity.
type SignedPI struct {
	PolymorphicIdentity PolymorphicIdentity `json:"polymorphicIdentity"`
	AuditElement        string              `json:"auditElement"`
	SigningKeyVersion   string              `json:"signingKeyVersion"`
	IssuanceDate        string              `json:"issuanceDate"`
}

// SignedPP is the signed part of a SignedPolymorphicPseudonym.
type SignedPP struct {
	PolymorphicPseudonym PolymorphicPseudonym `json:"polymorphicPseudonym"`
	AuditElement         string               `json:"auditElement"`
	SigningKeyVersion    string               `json:"signingKeyVersion"`
	IssuanceDate         string               `json:"issuanceDate"`
}

// SignedPolymorphicIdentity is a PI as it is issued.
type SignedPolymorphicIdentity struct {
	NotationIdentifier string         `json:"notationIdentifier"`
	SignedPI           SignedPI       `json:"signedPI"`
	SignatureValue     SignatureValue `json:"signatureValue"`
}

// SignedPolymorphicPseudonym is a PP as it is issued.
type SignedPolymorphicPseudonym struct {
	NotationIdentifier string         `json:"notationIdentifier"`
	SignedPP           SignedPP       `json:"signedPP"`
	SignatureValue     SignatureValue `json:"signatureValue"`
}

// EncryptedIdentity is an identity made for one party and one of its key
// sets.
type EncryptedIdentity struct {
	NotationIdentifier     string `json:"notationIdentifier"`
	SchemeVersion          string `json:"schemeVersion"`
	SchemeKeySetVersion    string `json:"schemeKeySetVersion"`
	Creator                string `json:"creator"`
	Recipient              string `json:"recipient"`
	RecipientKeySetVersion string `json:"recipientKeySetVersion"`
	// IdentityValue is the BSN. The real structure has curve points here.
	IdentityValue string `json:"identityValue"`
}

// EncryptedPseudonym is a pseudonym made for one party and one of its key
// sets.
type EncryptedPseudonym struct {
	NotationIdentifier     string `json:"notationIdentifier"`
	SchemeVersion          string `json:"schemeVersion"`
	SchemeKeySetVersion    string `json:"schemeKeySetVersion"`
	Creator                string `json:"creator"`
	Recipient              string `json:"recipient"`
	RecipientKeySetVersion string `json:"recipientKeySetVersion"`
	Type                   string `json:"type"`
	// PseudonymValue is the pseudonym of the citizen at the recipient. The
	// real structure has curve points here.
	PseudonymValue string `json:"pseudonymValue"`
}

// SignedEI is the signed part of a SignedEncryptedIdentity.
type SignedEI struct {
	EncryptedIdentity EncryptedIdentity `json:"encryptedIdentity"`
	AuditElement      string            `json:"auditElement"`
	IssuanceDate      string            `json:"issuanceDate"`
	ExtraElements     []ExtraElement    `json:"extraElements,omitempty"`
}

// SignedEP is the signed part of a SignedEncryptedPseudonym.
type SignedEP struct {
	EncryptedPseudonym EncryptedPseudonym `json:"encryptedPseudonym"`
	AuditElement       string             `json:"auditElement"`
	IssuanceDate       string             `json:"issuanceDate"`
	ExtraElements      []ExtraElement     `json:"extraElements,omitempty"`
}

// SignedEncryptedIdentity is a VI as it is issued.
type SignedEncryptedIdentity struct {
	NotationIdentifier string         `json:"notationIdentifier"`
	SignedEI           SignedEI       `json:"signedEI"`
	SignatureValue     SignatureValue `json:"signatureValue"`
}

// SignedEncryptedPseudonym is a VP as it is issued.
type SignedEncryptedPseudonym struct {
	NotationIdentifier string         `json:"notationIdentifier"`
	SignedEP           SignedEP       `json:"signedEP"`
	SignatureValue     SignatureValue `json:"signatureValue"`
}

// DecryptedPseudonym is what a party keeps after reading a VP.
type DecryptedPseudonym struct {
	NotationIdentifier  string `json:"notationIdentifier"`
	SchemeVersion       string `json:"schemeVersion"`
	SchemeKeySetVersion string `json:"schemeKeySetVersion"`
	Recipient           string `json:"recipient"`
	// RecipientKeySetVersion is the version of the closing key the
	// pseudonym was formed with.
	RecipientKeySetVersion string `json:"recipientKeySetVersion"`
	Type                   string `json:"type"`
	PseudonymValue         string `json:"pseudonymValue"`
}

// signature signs the signed part of a structure with a scheme key.
func signature(signatureType string, key []byte, signedPart any) SignatureValue {
	payload, _ := json.Marshal(signedPart) // structs of strings always marshal
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	sum := mac.Sum(nil)
	return SignatureValue{
		SignatureType: signatureType,
		R:             base64.StdEncoding.EncodeToString(sum[:16]),
		S:             base64.StdEncoding.EncodeToString(sum[16:]),
	}
}

// verified reports whether got is the signature of signedPart under key.
func verified(got SignatureValue, key []byte, signedPart any) bool {
	want := signature(got.SignatureType, key, signedPart)
	return hmac.Equal([]byte(got.R+got.S), []byte(want.R+want.S))
}

// encode returns a structure in its transport form.
func encode(structure any) string {
	payload, _ := json.Marshal(structure)
	return base64.StdEncoding.EncodeToString(payload)
}

var errNotAStructure = errors.New("not a base64 encoded structure")

// decode reads a transport form into a structure and checks that it is of
// the wanted kind. White space is ignored, since base64 is often wrapped.
func decode(encoded string, wantOID string, into any) error {
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(encoded), ""))
	if err != nil {
		return errNotAStructure
	}
	var head struct {
		NotationIdentifier string `json:"notationIdentifier"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return errNotAStructure
	}
	if head.NotationIdentifier != wantOID {
		return fmt.Errorf("notationIdentifier is %q, expected %q", head.NotationIdentifier, wantOID)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return errNotAStructure
	}
	return nil
}

// hash returns the base64 of the SHA-256 of the parts, joined with a
// separator that cannot occur in an OIN, a BSN or a version.
func hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{'|'})
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
