package polymorphic

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

const (
	consentService  = "99999999900000000900"
	broker          = "99999999900000000800"
	sourceX         = "99999999900000000200"
	sourceY         = "99999999900000000210"
	serviceProvider = "99999999900000000300"
	bsn             = "999991772"
	keys2026        = 20260101
	keys2026String  = "20260101"
)

// newMock returns a mock in which the two sources may receive the BSN and the
// service provider may not.
func newMock(t *testing.T) *Mock {
	t.Helper()
	m, err := New([]string{sourceX, sourceY})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func activate(t *testing.T, m *Mock) (pi, pp string) {
	t.Helper()
	pi, pp, err := m.Activate(Activation{Requester: consentService, RequesterKeySetVersion: 1, BSN: bsn}, false)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	return pi, pp
}

// keysOf returns the key files a party holds for one key set version.
func keysOf(t *testing.T, m *Mock, oin, version string) []string {
	t.Helper()
	keys, err := m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: oin, KeySetVersion: version})
	if err != nil {
		t.Fatalf("ProvideDVKeys(%s): %v", oin, err)
	}
	files := make([]string, 0, len(keys))
	for _, k := range keys {
		files = append(files, k.File)
	}
	return files
}

// readable decodes a value without any key, the way a test or a person would.
func readable(t *testing.T, encoded string) map[string]any {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("value is not base64: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("value is not JSON: %v", err)
	}
	return fields
}

func wantFault(t *testing.T, name string, err error, reason FaultReason) {
	t.Helper()
	var f *Fault
	if !errors.As(err, &f) || f.Reason != reason {
		t.Errorf("%s: err = %v, want fault %s", name, err, reason)
	}
}

func TestAValueIsReadableAndCarriesTheRealFieldNames(t *testing.T) {
	m := newMock(t)
	pi, pp := activate(t, m)

	signedPI := readable(t, pi)["signedPI"].(map[string]any)
	identity := signedPI["polymorphicIdentity"].(map[string]any)
	if identity["recipient"] != consentService || identity["recipientKeySetVersion"] != "1" || identity["identityValue"] != bsn {
		t.Errorf("polymorphicIdentity = %v, want the consent service as recipient and the BSN", identity)
	}
	if identity["creator"] != Creator || identity["schemeVersion"] != "1" || signedPI["issuanceDate"] == "" {
		t.Errorf("polymorphicIdentity = %v, signedPI = %v", identity, signedPI)
	}

	if strings.Contains(string(mustDecode(t, pp)), bsn) {
		t.Error("a polymorphic pseudonym must not carry the BSN")
	}
	pseudonym := readable(t, pp)["signedPP"].(map[string]any)["polymorphicPseudonym"].(map[string]any)
	if pseudonym["recipient"] != consentService || pseudonym["type"] != "B" {
		t.Errorf("polymorphicPseudonym = %v", pseudonym)
	}
}

func mustDecode(t *testing.T, encoded string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("value is not base64: %v", err)
	}
	return raw
}

func TestActivateIsStableUnlessRandomized(t *testing.T) {
	m := newMock(t)
	pi1, pp1 := activate(t, m)
	pi2, pp2 := activate(t, m)
	if pi1 != pi2 || pp1 != pp2 {
		t.Error("the same BSN and requester gave different values")
	}

	m.now = func() time.Time { return time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC) }
	a := Activation{Requester: consentService, RequesterKeySetVersion: 1, BSN: bsn}
	r1, _, err := m.Activate(a, true)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	r2, _, _ := m.Activate(a, true)
	if r1 == r2 {
		t.Error("randomized values are equal")
	}
	if got := readable(t, r1)["signedPI"].(map[string]any)["issuanceDate"]; got != "20260901" {
		t.Errorf("randomized issuanceDate = %v, want the first of the month, 20260901", got)
	}
}

func TestActivateValidatesItsInput(t *testing.T) {
	m := newMock(t)
	for name, a := range map[string]Activation{
		"a requester that is not an OIN": {Requester: "consent-service", RequesterKeySetVersion: 1, BSN: bsn},
		"no requester key set version":   {Requester: consentService, BSN: bsn},
		"a BSN of eight digits":          {Requester: consentService, RequesterKeySetVersion: 1, BSN: "12345678"},
		"a BSN with a letter":            {Requester: consentService, RequesterKeySetVersion: 1, BSN: "12345678a"},
	} {
		_, _, err := m.Activate(a, false)
		wantFault(t, name, err, SyntaxError)
	}
}

// A consent for a service with two sources and a service provider: every
// party gets a pseudonym, and the two that may have the BSN get an identity
// as well. That is two requests, because a party occurs once in a request.
func TestEveryPartyGetsAPseudonymAndAuthorisedPartiesAlsoAnIdentity(t *testing.T) {
	m := newMock(t)
	pi, pp := activate(t, m)

	pseudonyms, err := m.Transform(Transformation{
		Requester: consentService, PolymorphicPseudonym: pp,
		RelyingParties: []RelyingParty{
			{EntityID: sourceX, KeySetVersion: keys2026, IdentifierType: Pseudonym},
			{EntityID: sourceY, KeySetVersion: keys2026, IdentifierType: Pseudonym},
			{EntityID: serviceProvider, KeySetVersion: keys2026, IdentifierType: Pseudonym},
		},
	}, false)
	if err != nil {
		t.Fatalf("pseudonyms: %v", err)
	}
	identities, err := m.Transform(Transformation{
		Requester: consentService, PolymorphicIdentity: pi,
		RelyingParties: []RelyingParty{
			{EntityID: sourceX, KeySetVersion: keys2026, IdentifierType: Identity},
			{EntityID: sourceY, KeySetVersion: keys2026, IdentifierType: Identity},
		},
	}, false)
	if err != nil {
		t.Fatalf("identities: %v", err)
	}
	if len(pseudonyms)+len(identities) != 5 {
		t.Fatalf("got %d values, want 5", len(pseudonyms)+len(identities))
	}

	// Source X reads both of its values with its own keys.
	id, err := DecryptIdentity(identities[0].Value, keysOf(t, m, sourceX, keys2026String), SchemeKeys())
	if err != nil || id.BSN != bsn {
		t.Errorf("identity at source X: %+v, %v", id, err)
	}
	ps, err := DecryptPseudonym(pseudonyms[0].Value, keysOf(t, m, sourceX, keys2026String), SchemeKeys())
	if err != nil || ps.DecodedPseudonym.PseudonymValue == "" || ps.DecodedPseudonym.Recipient != sourceX {
		t.Errorf("pseudonym at source X: %+v, %v", ps, err)
	}

	// The service provider reads its pseudonym and never sees a BSN.
	sp, err := DecryptPseudonym(pseudonyms[2].Value, keysOf(t, m, serviceProvider, keys2026String), SchemeKeys())
	if err != nil || sp.DecodedPseudonym.PseudonymValue == "" {
		t.Errorf("pseudonym at the service provider: %+v, %v", sp, err)
	}
	if strings.Contains(string(mustDecode(t, pseudonyms[2].Value)), bsn) {
		t.Error("an encrypted pseudonym carries the BSN")
	}

	// Both kinds for one party do not fit in one request.
	_, err = m.Transform(Transformation{
		Requester: consentService, PolymorphicIdentity: pi, PolymorphicPseudonym: pp,
		RelyingParties: []RelyingParty{
			{EntityID: sourceX, KeySetVersion: keys2026, IdentifierType: Identity},
			{EntityID: sourceX, KeySetVersion: keys2026, IdentifierType: Pseudonym},
		},
	}, false)
	wantFault(t, "one party twice", err, SyntaxError)
}

func TestAPseudonymIsStablePerPartyAndKeySetAndDiffersOtherwise(t *testing.T) {
	m := newMock(t)
	_, pp := activate(t, m)

	pseudonymFor := func(oin string, version int, randomize bool) string {
		t.Helper()
		issued, err := m.Transform(Transformation{
			Requester: consentService, PolymorphicPseudonym: pp,
			RelyingParties: []RelyingParty{{EntityID: oin, KeySetVersion: version, IdentifierType: Pseudonym}},
		}, randomize)
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		d, err := DecryptPseudonym(issued[0].Value, keysOf(t, m, oin, itoa(version)), SchemeKeys())
		if err != nil {
			t.Fatalf("DecryptPseudonym: %v", err)
		}
		return d.DecodedPseudonym.PseudonymValue
	}

	base := pseudonymFor(serviceProvider, keys2026, false)
	if base != pseudonymFor(serviceProvider, keys2026, true) {
		t.Error("the pseudonym of one citizen at one party changed between requests")
	}
	if base == pseudonymFor(sourceX, keys2026, false) {
		t.Error("two parties got the same pseudonym for one citizen")
	}
	if base == pseudonymFor(serviceProvider, 20270101, false) {
		t.Error("the pseudonym did not change with the party's key set")
	}
}

func itoa(version int) string {
	b, _ := json.Marshal(version)
	return string(b)
}

func TestRandomizedValuesDifferAndDecryptTheSame(t *testing.T) {
	m := newMock(t)
	pi, _ := activate(t, m)
	req := Transformation{
		Requester: consentService, PolymorphicIdentity: pi,
		RelyingParties: []RelyingParty{{EntityID: sourceX, KeySetVersion: keys2026, IdentifierType: Identity}},
	}
	first, err := m.Transform(req, true)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	second, _ := m.Transform(req, true)
	if first[0].Value == second[0].Value {
		t.Fatal("randomized values are equal")
	}
	for _, issued := range [][]Encrypted{first, second} {
		d, err := DecryptIdentity(issued[0].Value, keysOf(t, m, sourceX, keys2026String), SchemeKeys())
		if err != nil || d.BSN != bsn {
			t.Errorf("DecryptIdentity = %+v, %v; want the BSN", d, err)
		}
	}
}

func TestTransformRefusals(t *testing.T) {
	m := newMock(t)
	pi, pp := activate(t, m)
	identityFor := func(oin string) []RelyingParty {
		return []RelyingParty{{EntityID: oin, KeySetVersion: keys2026, IdentifierType: Identity}}
	}
	five := make([]RelyingParty, 0, 5)
	for _, oin := range []string{"99999999900000000301", "99999999900000000302", "99999999900000000303", "99999999900000000304", "99999999900000000305"} {
		five = append(five, RelyingParty{EntityID: oin, KeySetVersion: keys2026, IdentifierType: Pseudonym})
	}

	for name, tc := range map[string]struct {
		req  Transformation
		want FaultReason
	}{
		"an identity for a party that may not have the BSN": {
			Transformation{Requester: consentService, PolymorphicIdentity: pi, RelyingParties: identityFor(serviceProvider)}, ProvisioningRefused},
		"a PI issued to another requester": {
			Transformation{Requester: serviceProvider, PolymorphicIdentity: pi, RelyingParties: identityFor(sourceX)}, AuthorizationError},
		"a PP where a PI is needed": {
			Transformation{Requester: consentService, PolymorphicIdentity: pp, RelyingParties: identityFor(sourceX)}, SyntaxError},
		"an identity without a PI": {
			Transformation{Requester: consentService, PolymorphicPseudonym: pp, RelyingParties: identityFor(sourceX)}, SyntaxError},
		"no relying parties": {
			Transformation{Requester: consentService, PolymorphicIdentity: pi}, SyntaxError},
		"five relying parties": {
			Transformation{Requester: consentService, PolymorphicPseudonym: pp, RelyingParties: five}, SyntaxError},
		"a key set version that is not a date": {
			Transformation{Requester: consentService, PolymorphicIdentity: pi,
				RelyingParties: []RelyingParty{{EntityID: sourceX, KeySetVersion: 1, IdentifierType: Identity}}}, SyntaxError},
		"no identifier type": {
			Transformation{Requester: consentService, PolymorphicIdentity: pi,
				RelyingParties: []RelyingParty{{EntityID: sourceX, KeySetVersion: keys2026}}}, SyntaxError},
		"a nonce with a hyphen": {
			Transformation{Requester: consentService, PolymorphicIdentity: pi,
				RelyingParties: []RelyingParty{{EntityID: sourceX, KeySetVersion: keys2026, IdentifierType: Identity, Nonce: "550e8400-e29b"}}}, SyntaxError},
	} {
		issued, err := m.Transform(tc.req, false)
		wantFault(t, name, err, tc.want)
		if len(issued) != 0 {
			t.Errorf("%s: issued %d values on a failed request", name, len(issued))
		}
	}
}

func TestOneUnservablePartyFailsTheWholeRequest(t *testing.T) {
	m := newMock(t)
	pi, _ := activate(t, m)
	issued, err := m.Transform(Transformation{
		Requester: consentService, PolymorphicIdentity: pi,
		RelyingParties: []RelyingParty{
			{EntityID: sourceX, KeySetVersion: keys2026, IdentifierType: Identity},
			{EntityID: serviceProvider, KeySetVersion: keys2026, IdentifierType: Identity},
		},
	}, false)
	wantFault(t, "second party unauthorised", err, ProvisioningRefused)
	if len(issued) != 0 {
		t.Errorf("issued %d values, want none", len(issued))
	}
}

func identityForX(t *testing.T, m *Mock, nonce string) string {
	t.Helper()
	pi, _ := activate(t, m)
	issued, err := m.Transform(Transformation{
		Requester: consentService, PolymorphicIdentity: pi,
		RelyingParties: []RelyingParty{{EntityID: sourceX, KeySetVersion: keys2026, IdentifierType: Identity, Nonce: nonce}},
	}, false)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	return issued[0].Value
}

func TestDecryptIdentityGivesTheBSNTheNonceAndTheDecodedInput(t *testing.T) {
	m := newMock(t)
	vi := identityForX(t, m, "consent0001")

	d, err := DecryptIdentity(vi, keysOf(t, m, sourceX, keys2026String), SchemeKeys())
	if err != nil {
		t.Fatalf("DecryptIdentity: %v", err)
	}
	inner := d.DecodedInput.SignedEI.EncryptedIdentity
	if d.BSN != bsn || inner.Recipient != sourceX || inner.RecipientKeySetVersion != keys2026String {
		t.Errorf("result = %+v", d)
	}
	if len(d.ExtraElements) != 1 || d.ExtraElements[0].Key != "RP:Nonce" || d.ExtraElements[0].Value != "consent0001" {
		t.Errorf("extraElements = %+v, want the nonce", d.ExtraElements)
	}
	if d.DecodedInput.NotationIdentifier != "2.16.528.1.1003.10.1.2.7.2" || d.IssuanceDate == "" {
		t.Errorf("decodedInput = %+v", d.DecodedInput)
	}
}

func TestDecryptIsOnlyForTheRecipientWithTheRightKeys(t *testing.T) {
	m := newMock(t)
	vi := identityForX(t, m, "")
	own := keysOf(t, m, sourceX, keys2026String)

	withOlderSetToo := append(keysOf(t, m, sourceX, "20250101"), own...)
	if _, err := DecryptIdentity(vi, withOlderSetToo, SchemeKeys()); err != nil {
		t.Errorf("with two key sets: %v", err)
	}

	for name, keys := range map[string][]string{
		"the keys of another party":         keysOf(t, m, sourceY, keys2026String),
		"a key set of another date":         keysOf(t, m, sourceX, "20270101"),
		"only the keys for a pseudonym":     own[:2],
		"no keys":                           nil,
		"something that is not a key file":  {"not a key"},
		"a key file without its headers":    {string(pem.EncodeToMemory(&pem.Block{Type: keyFileLabel, Bytes: keyFileBody}))},
		"a key file with a different label": {string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: keyFileBody}))},
	} {
		if _, err := DecryptIdentity(vi, keys, SchemeKeys()); err == nil {
			t.Errorf("%s: decrypted", name)
		}
	}

	if _, err := DecryptIdentity(vi, own, nil); err == nil {
		t.Error("without scheme keys: decrypted")
	}
	wrong := map[string]string{"urn:nl-gdi-eid:1.0:pp-key:mock:1:IP_P:1": base64.StdEncoding.EncodeToString([]byte("another key"))}
	if _, err := DecryptIdentity(vi, own, wrong); err == nil {
		t.Error("with another scheme key: decrypted")
	}
}

func TestAnAlteredOrMadeUpValueIsRejected(t *testing.T) {
	m := newMock(t)
	own := keysOf(t, m, sourceX, keys2026String)

	var ei SignedEncryptedIdentity
	if err := json.Unmarshal(mustDecode(t, identityForX(t, m, "")), &ei); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ei.SignedEI.EncryptedIdentity.IdentityValue = "123456789"
	if _, err := DecryptIdentity(encode(ei), own, SchemeKeys()); err == nil {
		t.Error("a value with another BSN written into it decrypted")
	}

	ei.SignatureValue = SignatureValue{}
	if _, err := DecryptIdentity(encode(ei), own, SchemeKeys()); err == nil {
		t.Error("a value without a signature decrypted")
	}

	pi, pp := activate(t, m)
	for name, value := range map[string]string{"a PI": pi, "a PP": pp, "garbage": "not base64 at all!", "empty": ""} {
		if _, err := DecryptIdentity(value, own, SchemeKeys()); err == nil {
			t.Errorf("%s decrypted as an identity", name)
		}
		if _, err := DecryptPseudonym(value, own, SchemeKeys()); err == nil {
			t.Errorf("%s decrypted as a pseudonym", name)
		}
	}
}

func TestABSNWithALeadingZeroIsEncodedWithoutIt(t *testing.T) {
	m := newMock(t)
	pi, _, err := m.Activate(Activation{Requester: consentService, RequesterKeySetVersion: 1, BSN: "012345678"}, false)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	issued, err := m.Transform(Transformation{
		Requester: consentService, PolymorphicIdentity: pi,
		RelyingParties: []RelyingParty{{EntityID: sourceX, KeySetVersion: keys2026, IdentifierType: Identity}},
	}, false)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	d, err := DecryptIdentity(issued[0].Value, keysOf(t, m, sourceX, keys2026String), SchemeKeys())
	if err != nil {
		t.Fatalf("DecryptIdentity: %v", err)
	}
	e := d.Encoded
	if d.BSN != "012345678" || e.Identifier != "12345678" || e.Version != "01" || e.Type != "B" || len(e.Bytes) != 18 ||
		e.Bytes[0] != 1 || e.Bytes[1] != 'B' || e.Bytes[2] != 8 || string(e.Bytes[3:11]) != "12345678" {
		t.Errorf("BSN %q, encoded %+v", d.BSN, e)
	}
}

func TestProvideDVKeys(t *testing.T) {
	m := newMock(t)
	types := func(keys []DVKey) string {
		names := make([]string, 0, len(keys))
		for _, k := range keys {
			names = append(names, k.KeyType)
		}
		return strings.Join(names, ", ")
	}

	for name, tc := range map[string]struct {
		req  KeyRequest
		want string
	}{
		"a party that may have the BSN":        {KeyRequest{Requester: broker, RelyingParty: sourceX, KeySetVersion: keys2026String}, "EP Decryption, EP Closing, EI Decryption"},
		"a party that may not":                 {KeyRequest{Requester: broker, RelyingParty: serviceProvider, KeySetVersion: keys2026String}, "EP Decryption, EP Closing"},
		"an authorised party that wants none":  {KeyRequest{Requester: broker, RelyingParty: sourceX, KeySetVersion: keys2026String, EIDecryptionKey: EIKeyWithout}, "EP Decryption, EP Closing"},
		"an authorised party that expects one": {KeyRequest{Requester: broker, RelyingParty: sourceX, KeySetVersion: keys2026String, EIDecryptionKey: EIKeyExpected}, "EP Decryption, EP Closing, EI Decryption"},
		"an unauthorised party, key optional":  {KeyRequest{Requester: broker, RelyingParty: serviceProvider, KeySetVersion: keys2026String, EIDecryptionKey: EIKeyOptional}, "EP Decryption, EP Closing"},
	} {
		keys, err := m.ProvideDVKeys(tc.req)
		if err != nil || types(keys) != tc.want {
			t.Errorf("%s: %s, %v; want %s", name, types(keys), err, tc.want)
		}
	}

	_, err := m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: serviceProvider, KeySetVersion: keys2026String, EIDecryptionKey: EIKeyExpected})
	wantFault(t, "an unauthorised party that expects the key", err, ProvisioningRefused)
	_, err = m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: sourceX, KeySetVersion: "2026"})
	wantFault(t, "a version that is not a date", err, InvalidRequest)
	_, err = m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: "source-x", KeySetVersion: keys2026String})
	wantFault(t, "a party that is not an OIN", err, InvalidRequest)
	_, err = m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: sourceX, KeySetVersion: keys2026String, EIDecryptionKey: "MAYBE"})
	wantFault(t, "an unknown option", err, InvalidRequest)
	_, err = m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: sourceX, KeySetVersion: keys2026String, SchemeKeySetVersion: "2"})
	wantFault(t, "another scheme key set", err, InvalidRequest)
	_, err = m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: sourceX})
	wantFault(t, "neither a certificate nor a version", err, InvalidRequest)
	_, err = m.ProvideDVKeys(KeyRequest{Requester: "broker", RelyingParty: sourceX, KeySetVersion: keys2026String})
	wantFault(t, "a broker that is not an OIN", err, SyntaxError)
}

func TestAKeyFileHasTheHeadersOfARealOne(t *testing.T) {
	m := newMock(t)
	keys, err := m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: sourceX, KeySetVersion: keys2026String})
	if err != nil {
		t.Fatalf("ProvideDVKeys: %v", err)
	}
	block, rest := pem.Decode([]byte(keys[2].File))
	if block == nil || len(rest) != 0 {
		t.Fatalf("key file is not one PEM block: %q", keys[2].File)
	}
	want := map[string]string{
		"SchemeVersion": "1", "SchemeKeySetVersion": "1", "Type": "EI Decryption",
		"Recipient": sourceX, "RecipientKeySetVersion": keys2026String,
	}
	if block.Type != "EC PRIVATE KEY" || len(block.Headers) != len(want) {
		t.Errorf("block type %q, headers %v", block.Type, block.Headers)
	}
	for k, v := range want {
		if block.Headers[k] != v {
			t.Errorf("header %s = %q, want %q", k, block.Headers[k], v)
		}
	}
}

// With the real BSNk a party's keys get the date its certificate was issued
// as their version, and the certificate must be the party's own.
func TestKeysTakeTheirVersionFromTheCertificate(t *testing.T) {
	m := newMock(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	certFor := func(serial string) []byte {
		t.Helper()
		template := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: "source.example", SerialNumber: serial},
			NotBefore:    time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC),
			NotAfter:     time.Date(2029, 3, 14, 9, 0, 0, 0, time.UTC),
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatalf("CreateCertificate: %v", err)
		}
		return der
	}

	keys, err := m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: sourceX, Certificate: certFor(sourceX)})
	if err != nil || len(keys) != 3 || keys[0].RecipientKeyVersion != "20260314" {
		t.Errorf("keys = %+v, %v; want three keys of version 20260314", keys, err)
	}
	_, err = m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: sourceY, Certificate: certFor(sourceX)})
	wantFault(t, "the certificate of another party", err, InvalidRequest)
	_, err = m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: sourceX, Certificate: certFor("not an OIN")})
	wantFault(t, "a certificate without an OIN", err, InvalidRequest)
	_, err = m.ProvideDVKeys(KeyRequest{Requester: broker, RelyingParty: sourceX, Certificate: []byte("not a certificate")})
	wantFault(t, "not a certificate", err, InvalidRequest)
}

func TestSchemeKeysAreKeyedByURN(t *testing.T) {
	keys := SchemeKeys()
	for _, urn := range []string{"urn:nl-gdi-eid:1.0:pp-key:mock:1:IP_P:1", "urn:nl-gdi-eid:1.0:pp-key:mock:1:PP_P:1"} {
		if keys[urn] == "" {
			t.Errorf("no scheme key %s in %v", urn, keys)
		}
	}
}

func TestNewRejectsAMalformedOIN(t *testing.T) {
	if _, err := New([]string{"bd-mock"}); err == nil {
		t.Error("New accepted a name as an OIN")
	}
}

func TestBSNAuthorisedOINsIsSorted(t *testing.T) {
	m, err := New([]string{sourceY, sourceX})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := m.BSNAuthorisedOINs()
	if len(got) != 2 || got[0] != sourceX || got[1] != sourceY {
		t.Errorf("BSNAuthorisedOINs = %v", got)
	}
}
