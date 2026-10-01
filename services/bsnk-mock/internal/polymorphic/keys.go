package polymorphic

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// The kinds of key a receiving party holds.
const (
	KeyEIDecryption = "EI Decryption"
	KeyEPDecryption = "EP Decryption"
	KeyEPClosing    = "EP Closing"
)

// What a key request says about the key that reads a BSN.
const (
	// EIKeyOptional gives the key when the party may receive the BSN, and
	// leaves it out when it may not. This is the default.
	EIKeyOptional = "EI_DECRYPTION_KEY_OPTIONAL"
	// EIKeyExpected gives the key, and refuses the request when the party
	// may not receive the BSN.
	EIKeyExpected = "EI_DECRYPTION_KEY_EXPECTED"
	// EIKeyWithout never gives the key.
	EIKeyWithout = "WITHOUT_EI_DECRYPTION_KEY"
)

// Key file headers. The headers are those of a real key file, so that a
// component that reads the one reads the other. The label is not: a real key
// file is labelled EC PRIVATE KEY, and a mock key file holds no key. Its own
// label says so, to a reader and to a secret scanner alike.
const (
	keyFileLabel          = "BSNK MOCK DV KEY"
	headerSchemeVersion   = "SchemeVersion"
	headerSchemeKeySet    = "SchemeKeySetVersion"
	headerSchemeKeySetOld = "SchemeKeyVersion"
	headerType            = "Type"
	headerRecipient       = "Recipient"
	headerRecipientKeySet = "RecipientKeySetVersion"
)

// keyFileBody is what a mock key file holds where a real one holds a private
// key. A key of the mock is its headers: the party it is for, the key set
// version and the kind.
var keyFileBody = []byte("bsnk-mock: no key material")

// KeyRequest asks for the keys of one receiving party.
type KeyRequest struct {
	// Requester is the OIN of the broker that asks on the party's behalf.
	Requester string
	// RelyingParty is the party's OIN.
	RelyingParty string
	// Certificate is the party's certificate, in DER. The keys get the date
	// it was issued as their version, and its subject must carry the
	// party's OIN as serial number.
	Certificate []byte
	// KeySetVersion names the version, as YYYYMMDD, when there is no
	// certificate. The real BSNk has no such field; it is there so that a
	// test does not need a certificate.
	KeySetVersion string
	// SchemeKeySetVersion is optional. The mock has one scheme key set.
	SchemeKeySetVersion string
	// EIDecryptionKey is EIKeyOptional, EIKeyExpected or EIKeyWithout. Empty
	// means EIKeyOptional.
	EIDecryptionKey string
}

// DVKey is one key of a receiving party.
type DVKey struct {
	KeyType             string
	RecipientKeyVersion string
	SchemeKeySetVersion string
	// File is the key file, in PEM.
	File string
}

// ProvideDVKeys issues the keys with which a party reads its values: the two
// for a pseudonym, and the one for a BSN if the party may receive it.
func (m *Mock) ProvideDVKeys(r KeyRequest) ([]DVKey, error) {
	if !oinPattern.MatchString(r.Requester) {
		return nil, fault(SyntaxError, "Requester must be an OIN of 20 digits")
	}
	if !oinPattern.MatchString(r.RelyingParty) {
		return nil, fault(InvalidRequest, "RelyingParty must be an OIN of 20 digits")
	}
	if r.SchemeKeySetVersion != "" && r.SchemeKeySetVersion != SchemeKeySetVersion {
		return nil, fault(InvalidRequest, "the only scheme key set version is %s", SchemeKeySetVersion)
	}
	if r.Certificate != nil {
		oin, issued, err := certificateSubject(r.Certificate)
		if err != nil {
			return nil, err
		}
		if oin != r.RelyingParty {
			return nil, fault(InvalidRequest, "the certificate is not of the relying party")
		}
		r.KeySetVersion = issued
	}
	if !validKeySetVersion(r.KeySetVersion) {
		return nil, fault(InvalidRequest, "a certificate is required, or a key set version as YYYYMMDD")
	}

	types := []string{KeyEPDecryption, KeyEPClosing}
	switch r.EIDecryptionKey {
	case "", EIKeyOptional:
		if m.bsnAuthorised[r.RelyingParty] {
			types = append(types, KeyEIDecryption)
		}
	case EIKeyExpected:
		if !m.bsnAuthorised[r.RelyingParty] {
			return nil, fault(ProvisioningRefused, "relying party %s is not authorised to receive the BSN", r.RelyingParty)
		}
		types = append(types, KeyEIDecryption)
	case EIKeyWithout:
	default:
		return nil, fault(InvalidRequest, "ProvideEIDecryptionKey must be %s, %s or %s", EIKeyExpected, EIKeyOptional, EIKeyWithout)
	}

	keys := make([]DVKey, 0, len(types))
	for _, t := range types {
		file := pem.EncodeToMemory(&pem.Block{
			Type: keyFileLabel,
			Headers: map[string]string{
				headerSchemeVersion:   SchemeVersion,
				headerSchemeKeySet:    SchemeKeySetVersion,
				headerType:            t,
				headerRecipient:       r.RelyingParty,
				headerRecipientKeySet: r.KeySetVersion,
			},
			Bytes: keyFileBody,
		})
		keys = append(keys, DVKey{KeyType: t, RecipientKeyVersion: r.KeySetVersion, SchemeKeySetVersion: SchemeKeySetVersion, File: string(file)})
	}
	return keys, nil
}

// certificateSubject reads from a party's certificate what a key request
// takes from it: the OIN in the subject's serial number, and the date the
// certificate was issued, which becomes the key set version.
func certificateSubject(der []byte) (oin, keySetVersion string, err error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "", "", fault(InvalidRequest, "RelyingPartyPKIoCertificate is not an X.509 certificate")
	}
	if !oinPattern.MatchString(cert.Subject.SerialNumber) {
		return "", "", fault(InvalidRequest, "the certificate's subject serial number is not an OIN of 20 digits")
	}
	return cert.Subject.SerialNumber, cert.NotBefore.UTC().Format("20060102"), nil
}

// serviceProviderKey is what the mock reads from a key file.
type serviceProviderKey struct {
	keyType, recipient, recipientKeySetVersion, schemeKeySetVersion string
}

// parseKeyFiles reads the headers of the given key files.
func parseKeyFiles(files []string) ([]serviceProviderKey, error) {
	keys := make([]serviceProviderKey, 0, len(files))
	for i, f := range files {
		block, _ := pem.Decode([]byte(f))
		if block == nil || block.Type != keyFileLabel {
			return nil, fmt.Errorf("serviceProviderKeys[%d] is not a key file", i)
		}
		k := serviceProviderKey{
			keyType:                block.Headers[headerType],
			recipient:              block.Headers[headerRecipient],
			recipientKeySetVersion: block.Headers[headerRecipientKeySet],
			schemeKeySetVersion:    block.Headers[headerSchemeKeySet],
		}
		if k.schemeKeySetVersion == "" {
			k.schemeKeySetVersion = block.Headers[headerSchemeKeySetOld]
		}
		if k.keyType == "" || k.recipient == "" || k.recipientKeySetVersion == "" || k.schemeKeySetVersion == "" {
			return nil, fmt.Errorf("serviceProviderKeys[%d] lacks a required header", i)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

var errNoMatchingKey = errors.New("no matching key")

// requireKey checks that the party holds a key of the given kind for this
// recipient, key set version and scheme key set version.
func requireKey(keys []serviceProviderKey, keyType, recipient, recipientKeySetVersion, schemeKeySetVersion string) error {
	for _, k := range keys {
		if k.keyType == keyType && k.recipient == recipient &&
			k.recipientKeySetVersion == recipientKeySetVersion && k.schemeKeySetVersion == schemeKeySetVersion {
			return nil
		}
	}
	return fmt.Errorf("%w: need an %q key for recipient %s, key set version %s, scheme key set version %s",
		errNoMatchingKey, keyType, recipient, recipientKeySetVersion, schemeKeySetVersion)
}
