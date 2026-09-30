package polymorphic

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// FaultReason names why BSNk does not carry out a request.
type FaultReason string

const (
	// SyntaxError: the request cannot be understood.
	SyntaxError FaultReason = "SyntaxError"
	// InvalidRequest: a parameter of a key request is not acceptable.
	InvalidRequest FaultReason = "InvalidRequest"
	// AuthorizationError: the requester may not do this.
	AuthorizationError FaultReason = "AuthorizationError"
	// ProvisioningRefused: the request is understood and BSNk declines it.
	ProvisioningRefused FaultReason = "ProvisioningRefused"
)

// Fault is a refused request, with the reason BSNk gives for it.
type Fault struct {
	Reason      FaultReason
	Description string
}

func (f *Fault) Error() string { return string(f.Reason) + ": " + f.Description }

func fault(reason FaultReason, format string, args ...any) error {
	return &Fault{Reason: reason, Description: fmt.Sprintf(format, args...)}
}

// MaxRelyingParties is the number of parties one transformation serves. A
// service with more parties takes more than one request.
const MaxRelyingParties = 4

// What a relying party receives from a transformation.
const (
	// Identity gives the party a VI, from which it reads the BSN.
	Identity = "Identity"
	// Pseudonym gives the party a VP, from which it reads its own pseudonym.
	Pseudonym = "Pseudonym"
)

var (
	oinPattern   = regexp.MustCompile(`^[0-9]{20}$`)
	bsnPattern   = regexp.MustCompile(`^[0-9]{9}$`)
	noncePattern = regexp.MustCompile(`^[A-Za-z0-9]{8,128}$`)
)

// Mock stands in for BSNk. It keeps no state: everything it needs is in the
// values themselves.
type Mock struct {
	bsnAuthorised map[string]bool
	now           func() time.Time
}

// New returns a mock in which the given OINs may receive the BSN. Every other
// party can only receive a pseudonym.
func New(bsnAuthorisedOINs []string) (*Mock, error) {
	m := &Mock{bsnAuthorised: map[string]bool{}, now: time.Now}
	for _, oin := range bsnAuthorisedOINs {
		if !oinPattern.MatchString(oin) {
			return nil, fmt.Errorf("%q is not an OIN of 20 digits", oin)
		}
		m.bsnAuthorised[oin] = true
	}
	return m, nil
}

// BSNAuthorisedOINs lists the parties that may receive the BSN, sorted. A
// requester looks a party up here before it asks for an identity.
func (m *Mock) BSNAuthorisedOINs() []string {
	oins := make([]string, 0, len(m.bsnAuthorised))
	for oin := range m.bsnAuthorised {
		oins = append(oins, oin)
	}
	sort.Strings(oins)
	return oins
}

// issuance returns the audit element and issuance date of a new value.
// Without randomisation they are fixed, so the same request always gives the
// same value. With it the audit element is random and the date is the first
// of the current month, so two values for the same citizen and party differ,
// as they do with the real BSNk.
func (m *Mock) issuance(randomize bool) (auditElement, issuanceDate string) {
	if !randomize {
		return stableAuditElement, stableIssuanceDate
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail on supported platforms
	return base64.StdEncoding.EncodeToString(b), m.now().UTC().Format("200601") + "01"
}

// Activation is a request to activate a BSN.
type Activation struct {
	Requester              string
	RequesterKeySetVersion int
	BSN                    string
}

// Activate turns a BSN into a signed PI and a signed PP for the requester.
// Only that requester can have them transformed.
func (m *Mock) Activate(a Activation, randomize bool) (pi, pp string, err error) {
	if !oinPattern.MatchString(a.Requester) {
		return "", "", fault(SyntaxError, "Requester must be an OIN of 20 digits")
	}
	if a.RequesterKeySetVersion < 1 {
		return "", "", fault(SyntaxError, "RequesterKeySetVersion must be a positive integer")
	}
	if !bsnPattern.MatchString(a.BSN) {
		return "", "", fault(SyntaxError, "BSN must be 9 digits, padded with a leading zero")
	}
	version := strconv.Itoa(a.RequesterKeySetVersion)

	audit, date := m.issuance(randomize)
	signedPI := SignedPI{
		PolymorphicIdentity: PolymorphicIdentity{
			NotationIdentifier: oidPolymorphicIdentity, SchemeVersion: SchemeVersion, SchemeKeySetVersion: SchemeKeySetVersion,
			Creator: Creator, Recipient: a.Requester, RecipientKeySetVersion: version,
			IdentityValue: a.BSN,
		},
		AuditElement: audit, SigningKeyVersion: signingKeyVersion, IssuanceDate: date,
	}
	audit, date = m.issuance(randomize)
	signedPP := SignedPP{
		PolymorphicPseudonym: PolymorphicPseudonym{
			NotationIdentifier: oidPolymorphicPseudonym, SchemeVersion: SchemeVersion, SchemeKeySetVersion: SchemeKeySetVersion,
			Creator: Creator, Recipient: a.Requester, RecipientKeySetVersion: version,
			Type: pseudonymTypeBSN, PseudonymSeed: hash("seed", a.BSN),
		},
		AuditElement: audit, SigningKeyVersion: signingKeyVersion, IssuanceDate: date,
	}
	pi = encode(SignedPolymorphicIdentity{
		NotationIdentifier: oidSignedPolymorphicIdentity, SignedPI: signedPI,
		SignatureValue: signature(oidECDSA, polymorphicSigningKey, signedPI),
	})
	pp = encode(SignedPolymorphicPseudonym{
		NotationIdentifier: oidSignedPolymorphicPseudonym, SignedPP: signedPP,
		SignatureValue: signature(oidECDSA, polymorphicSigningKey, signedPP),
	})
	return pi, pp, nil
}

// RelyingParty is one receiver in a transformation.
type RelyingParty struct {
	// EntityID is the party's OIN.
	EntityID string
	// KeySetVersion is the version of the party's keys the value is made
	// for: the date its keys were issued under, as the number YYYYMMDD.
	KeySetVersion int
	// IdentifierType says what this party gets: Identity or Pseudonym. A
	// party that needs both takes two requests.
	IdentifierType string
	// Nonce is optional. It is signed into the value, so a caller can tie
	// the value to something of its own, such as a consent.
	Nonce string
}

// Transformation is a request to turn a PI or PP into values for parties.
type Transformation struct {
	Requester string
	// PolymorphicIdentity is needed when a party asks for an identity,
	// PolymorphicPseudonym when one asks for a pseudonym.
	PolymorphicIdentity, PolymorphicPseudonym string
	RelyingParties                            []RelyingParty
}

// Encrypted is the value made for one party.
type Encrypted struct {
	EntityID       string
	KeySetVersion  int
	IdentifierType string
	Value          string
}

// Transform makes a VI or VP for every party in the request. If one party
// cannot be served the whole request fails and nothing is issued.
func (m *Mock) Transform(t Transformation, randomize bool) ([]Encrypted, error) {
	if !oinPattern.MatchString(t.Requester) {
		return nil, fault(SyntaxError, "Requester must be an OIN of 20 digits")
	}
	if len(t.RelyingParties) == 0 {
		return nil, fault(SyntaxError, "at least one RelyingParty is required")
	}
	if len(t.RelyingParties) > MaxRelyingParties {
		return nil, fault(SyntaxError, "at most %d relying parties per request, got %d", MaxRelyingParties, len(t.RelyingParties))
	}

	var bsn, seed *string
	seen := map[string]bool{}
	out := make([]Encrypted, 0, len(t.RelyingParties))
	for _, rp := range t.RelyingParties {
		if err := validateRelyingParty(rp); err != nil {
			return nil, err
		}
		if seen[rp.EntityID] {
			return nil, fault(SyntaxError, "EntityID %s occurs more than once", rp.EntityID)
		}
		seen[rp.EntityID] = true

		version := strconv.Itoa(rp.KeySetVersion)
		audit, date := m.issuance(randomize)
		var extra []ExtraElement
		if rp.Nonce != "" {
			extra = []ExtraElement{{Key: nonceKey, Value: rp.Nonce}}
		}

		var value string
		switch rp.IdentifierType {
		case Identity:
			if !m.bsnAuthorised[rp.EntityID] {
				return nil, fault(ProvisioningRefused, "relying party %s is not authorised to receive the BSN", rp.EntityID)
			}
			if bsn == nil {
				pi, err := ownIdentity(t.PolymorphicIdentity, t.Requester)
				if err != nil {
					return nil, err
				}
				bsn = &pi.IdentityValue
			}
			signed := SignedEI{
				EncryptedIdentity: EncryptedIdentity{
					NotationIdentifier: oidEncryptedIdentity, SchemeVersion: SchemeVersion, SchemeKeySetVersion: SchemeKeySetVersion,
					Creator: Creator, Recipient: rp.EntityID, RecipientKeySetVersion: version,
					IdentityValue: *bsn,
				},
				AuditElement: audit, IssuanceDate: date, ExtraElements: extra,
			}
			value = encode(SignedEncryptedIdentity{
				NotationIdentifier: oidSignedEncryptedIdentity, SignedEI: signed,
				SignatureValue: signature(oidECSDSA, identitySchemeKey, signed),
			})
		case Pseudonym:
			if seed == nil {
				pp, err := ownPseudonym(t.PolymorphicPseudonym, t.Requester)
				if err != nil {
					return nil, err
				}
				seed = &pp.PseudonymSeed
			}
			signed := SignedEP{
				EncryptedPseudonym: EncryptedPseudonym{
					NotationIdentifier: oidEncryptedPseudonym, SchemeVersion: SchemeVersion, SchemeKeySetVersion: SchemeKeySetVersion,
					Creator: Creator, Recipient: rp.EntityID, RecipientKeySetVersion: version,
					Type: pseudonymTypeBSN, PseudonymValue: hash("pseudonym", *seed, rp.EntityID, version),
				},
				AuditElement: audit, IssuanceDate: date, ExtraElements: extra,
			}
			value = encode(SignedEncryptedPseudonym{
				NotationIdentifier: oidSignedEncryptedPseudonym, SignedEP: signed,
				SignatureValue: signature(oidECSDSA, pseudonymSchemeKey, signed),
			})
		}
		out = append(out, Encrypted{EntityID: rp.EntityID, KeySetVersion: rp.KeySetVersion, IdentifierType: rp.IdentifierType, Value: value})
	}
	return out, nil
}

func validateRelyingParty(rp RelyingParty) error {
	if !oinPattern.MatchString(rp.EntityID) {
		return fault(SyntaxError, "EntityID must be an OIN of 20 digits, got %q", rp.EntityID)
	}
	if !validKeySetVersion(strconv.Itoa(rp.KeySetVersion)) {
		return fault(SyntaxError, "KeySetVersion of %s must be a date as the number YYYYMMDD", rp.EntityID)
	}
	if rp.IdentifierType != Identity && rp.IdentifierType != Pseudonym {
		return fault(SyntaxError, "IdentifierType of %s must be %q or %q", rp.EntityID, Identity, Pseudonym)
	}
	if rp.Nonce != "" && !noncePattern.MatchString(rp.Nonce) {
		return fault(SyntaxError, "Nonce of %s must be 8 to 128 letters and digits", rp.EntityID)
	}
	return nil
}

// validKeySetVersion reports whether s is a calendar date written as
// YYYYMMDD, which is what the key set version of a service provider is.
func validKeySetVersion(s string) bool {
	if len(s) != 8 {
		return false
	}
	_, err := time.Parse("20060102", s)
	return err == nil
}

// ownIdentity reads a PI and checks that the mock issued it to this
// requester.
func ownIdentity(encoded, requester string) (PolymorphicIdentity, error) {
	if encoded == "" {
		return PolymorphicIdentity{}, fault(SyntaxError, "PolymorphicIdentity is required when a relying party asks for an identity")
	}
	var pi SignedPolymorphicIdentity
	if err := decode(encoded, oidSignedPolymorphicIdentity, &pi); err != nil {
		return PolymorphicIdentity{}, fault(SyntaxError, "PolymorphicIdentity: %v", err)
	}
	if !verified(pi.SignatureValue, polymorphicSigningKey, pi.SignedPI) {
		return PolymorphicIdentity{}, fault(SyntaxError, "PolymorphicIdentity: the signature does not verify")
	}
	if pi.SignedPI.PolymorphicIdentity.Recipient != requester {
		return PolymorphicIdentity{}, fault(AuthorizationError, "this polymorphic identity was issued to another requester")
	}
	return pi.SignedPI.PolymorphicIdentity, nil
}

// ownPseudonym reads a PP and checks that the mock issued it to this
// requester.
func ownPseudonym(encoded, requester string) (PolymorphicPseudonym, error) {
	if encoded == "" {
		return PolymorphicPseudonym{}, fault(SyntaxError, "PolymorphicPseudonym is required when a relying party asks for a pseudonym")
	}
	var pp SignedPolymorphicPseudonym
	if err := decode(encoded, oidSignedPolymorphicPseudonym, &pp); err != nil {
		return PolymorphicPseudonym{}, fault(SyntaxError, "PolymorphicPseudonym: %v", err)
	}
	if !verified(pp.SignatureValue, polymorphicSigningKey, pp.SignedPP) {
		return PolymorphicPseudonym{}, fault(SyntaxError, "PolymorphicPseudonym: the signature does not verify")
	}
	if pp.SignedPP.PolymorphicPseudonym.Recipient != requester {
		return PolymorphicPseudonym{}, fault(AuthorizationError, "this polymorphic pseudonym was issued to another requester")
	}
	return pp.SignedPP.PolymorphicPseudonym, nil
}
