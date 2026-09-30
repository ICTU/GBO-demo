package polymorphic

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Reading a value is what a receiving party does itself, with its own keys
// and without calling BSNk. The two functions here stand in for the component
// a party runs for that. They take what that component takes: the value, the
// party's key files and the scheme keys.

// IdentityResult is what a party reads from a VI.
type IdentityResult struct {
	DecodedInput SignedEncryptedIdentity
	// BSN is nine digits, padded with a leading zero.
	BSN           string
	Encoded       EncodedIdentity
	IssuanceDate  string
	ExtraElements []ExtraElement
}

// EncodedIdentity is the identity as the decrypted value encodes it.
type EncodedIdentity struct {
	// Bytes is a version byte, a type byte, a length byte and the
	// identifier, padded with zero bytes to 18.
	Bytes []byte
	// Version is the version of this encoding.
	Version string
	// Type says what kind of identifier this is; B is a BSN.
	Type string
	// Identifier is the BSN without leading zeros.
	Identifier string
}

func encodeIdentity(bsn string) EncodedIdentity {
	identifier := strings.TrimLeft(bsn, "0")
	encoded := make([]byte, 18)
	encoded[0], encoded[1], encoded[2] = 1, pseudonymTypeBSN[0], byte(len(identifier))
	copy(encoded[3:], identifier)
	return EncodedIdentity{Bytes: encoded, Version: "01", Type: pseudonymTypeBSN, Identifier: identifier}
}

// DecryptIdentity reads a VI. The party must hold an EI Decryption key for
// the recipient and key set version the value was made for, and must supply
// the scheme key that verifies it.
func DecryptIdentity(signedEncryptedIdentity string, serviceProviderKeys []string, schemeKeys map[string]string) (IdentityResult, error) {
	var ei SignedEncryptedIdentity
	if err := decode(signedEncryptedIdentity, oidSignedEncryptedIdentity, &ei); err != nil {
		return IdentityResult{}, fmt.Errorf("signedEncryptedIdentity: %w", err)
	}
	inner := ei.SignedEI.EncryptedIdentity
	if err := checkScheme(inner.NotationIdentifier, oidEncryptedIdentity, inner.SchemeVersion); err != nil {
		return IdentityResult{}, err
	}
	key, err := schemeKey(schemeKeys, schemeKeyIdentity, inner.SchemeKeySetVersion)
	if err != nil {
		return IdentityResult{}, err
	}
	if !verified(ei.SignatureValue, key, ei.SignedEI) {
		return IdentityResult{}, errors.New("the signature on the encrypted identity does not verify")
	}
	keys, err := parseKeyFiles(serviceProviderKeys)
	if err != nil {
		return IdentityResult{}, err
	}
	if err := requireKey(keys, KeyEIDecryption, inner.Recipient, inner.RecipientKeySetVersion, inner.SchemeKeySetVersion); err != nil {
		return IdentityResult{}, err
	}
	return IdentityResult{
		DecodedInput:  ei,
		BSN:           inner.IdentityValue,
		Encoded:       encodeIdentity(inner.IdentityValue),
		IssuanceDate:  ei.SignedEI.IssuanceDate,
		ExtraElements: ei.SignedEI.ExtraElements,
	}, nil
}

// PseudonymResult is what a party reads from a VP.
type PseudonymResult struct {
	DecodedInput SignedEncryptedPseudonym
	// Pseudonym is DecodedPseudonym in its transport form, which is what a
	// party stores.
	Pseudonym        string
	DecodedPseudonym DecryptedPseudonym
	IssuanceDate     string
	ExtraElements    []ExtraElement
}

// DecryptPseudonym reads a VP. The party must hold an EP Decryption key and
// an EP Closing key for the recipient and key set version the value was made
// for, and must supply the scheme key that verifies it.
func DecryptPseudonym(signedEncryptedPseudonym string, serviceProviderKeys []string, schemeKeys map[string]string) (PseudonymResult, error) {
	var ep SignedEncryptedPseudonym
	if err := decode(signedEncryptedPseudonym, oidSignedEncryptedPseudonym, &ep); err != nil {
		return PseudonymResult{}, fmt.Errorf("signedEncryptedPseudonym: %w", err)
	}
	inner := ep.SignedEP.EncryptedPseudonym
	if err := checkScheme(inner.NotationIdentifier, oidEncryptedPseudonym, inner.SchemeVersion); err != nil {
		return PseudonymResult{}, err
	}
	key, err := schemeKey(schemeKeys, schemeKeyPseudonym, inner.SchemeKeySetVersion)
	if err != nil {
		return PseudonymResult{}, err
	}
	if !verified(ep.SignatureValue, key, ep.SignedEP) {
		return PseudonymResult{}, errors.New("the signature on the encrypted pseudonym does not verify")
	}
	keys, err := parseKeyFiles(serviceProviderKeys)
	if err != nil {
		return PseudonymResult{}, err
	}
	for _, keyType := range []string{KeyEPDecryption, KeyEPClosing} {
		if err := requireKey(keys, keyType, inner.Recipient, inner.RecipientKeySetVersion, inner.SchemeKeySetVersion); err != nil {
			return PseudonymResult{}, err
		}
	}
	decoded := DecryptedPseudonym{
		NotationIdentifier: oidDecryptedPseudonym, SchemeVersion: inner.SchemeVersion, SchemeKeySetVersion: inner.SchemeKeySetVersion,
		Recipient: inner.Recipient, RecipientKeySetVersion: inner.RecipientKeySetVersion,
		Type: inner.Type, PseudonymValue: inner.PseudonymValue,
	}
	return PseudonymResult{
		DecodedInput:     ep,
		Pseudonym:        encode(decoded),
		DecodedPseudonym: decoded,
		IssuanceDate:     ep.SignedEP.IssuanceDate,
		ExtraElements:    ep.SignedEP.ExtraElements,
	}, nil
}

func checkScheme(gotOID, wantOID, schemeVersion string) error {
	if gotOID != wantOID {
		return fmt.Errorf("inner notationIdentifier is %q, expected %q", gotOID, wantOID)
	}
	if schemeVersion != SchemeVersion {
		return fmt.Errorf("schemeVersion %q is not supported", schemeVersion)
	}
	return nil
}

// schemeKey finds, among the scheme keys a party supplied, the one of the
// given type and scheme key set version, and checks that it is the mock's. A
// scheme key URN has the form
// urn:nl-gdi-eid:1.0:pp-key:<environment>:<schemeKeySetVersion>:<type>:<version>.
func schemeKey(schemeKeys map[string]string, keyType, schemeKeySetVersion string) ([]byte, error) {
	want := identitySchemeKey
	if keyType == schemeKeyPseudonym {
		want = pseudonymSchemeKey
	}
	for urn, value := range schemeKeys {
		parts := strings.Split(urn, ":")
		if len(parts) != 8 || parts[5] != schemeKeySetVersion || parts[6] != keyType {
			continue
		}
		got, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(value), ""))
		if err != nil || !bytes.Equal(got, want) {
			return nil, fmt.Errorf("scheme key %s is not the key this value was signed with", urn)
		}
		return got, nil
	}
	return nil, fmt.Errorf("schemeKeys has no %s key for scheme key set version %s", keyType, schemeKeySetVersion)
}
