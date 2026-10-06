package substitution

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	ownOIN   = "99999999900000000200"
	otherOIN = "99999999900000000210"
	bsn      = "999991772"
)

// pseudonym is what the fake reads from a pseudonym made for this source.
const pseudonym = "pseudonym-of-the-citizen-at-the-source"

// fakeDecrypter reads the test's own values: "for:<oin>" decrypts to the BSN
// or the pseudonym, with that OIN as the recipient the value names.
type fakeDecrypter struct {
	got []string
	err error
}

func (f *fakeDecrypter) Identity(_ context.Context, value string) (Identity, error) {
	f.got = append(f.got, "identity "+value)
	if f.err != nil {
		return Identity{}, f.err
	}
	return Identity{BSN: bsn, Recipient: strings.TrimPrefix(value, "for:")}, nil
}

func (f *fakeDecrypter) Pseudonym(_ context.Context, value string) (Pseudonym, error) {
	f.got = append(f.got, "pseudonym "+value)
	if f.err != nil {
		return Pseudonym{}, f.err
	}
	return Pseudonym{Value: pseudonym, Recipient: strings.TrimPrefix(value, "for:")}, nil
}

// token builds an unsigned consent token with a value per party. The
// substitution does not verify the token; the PDP did.
func token(t *testing.T, subject map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"consent_id": "c-1", "encrypted_subject": subject})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	return encode([]byte(`{"alg":"ES256"}`)) + "." + encode(payload) + ".signature"
}

// valuesFor is what the token carries for a party on the BSN authorisation
// list: an identity and a pseudonym, both made for recipient.
func valuesFor(recipient string) map[string]any {
	return map[string]any{
		"identity":  map[string]any{"key_set_version": 20260101, "value": "for:" + recipient},
		"pseudonym": map[string]any{"key_set_version": 20260101, "value": "for:" + recipient},
	}
}

// pseudonymOnlyFor is what the token carries for a party not on the list.
func pseudonymOnlyFor(recipient string) map[string]any {
	return map[string]any{"pseudonym": map[string]any{"key_set_version": 20260101, "value": "for:" + recipient}}
}

func substituter(d Decrypter) Substituter {
	return Substituter{OwnOIN: ownOIN, Variables: []string{"bsn"}, Decrypter: d}
}

const query = `{"query":"query($bsn: BSN!, $vboId: String!) { vbo(bsn: $bsn, vboId: $vboId) { vboId } }","variables":{"bsn":"consent:identity","vboId":"0632010000099412"},"operationName":"Eigendom"}`

func TestThePlaceholderBecomesTheBSNOfTheConsent(t *testing.T) {
	decrypter := &fakeDecrypter{}
	tok := token(t, map[string]any{ownOIN: valuesFor(ownOIN), otherOIN: valuesFor(otherOIN)})

	result, err := substituter(decrypter).Apply(context.Background(), []byte(query), tok)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.BSN != bsn || result.ConsentID != "c-1" {
		t.Errorf("result = %+v, want the BSN and the consent id", result)
	}
	// The source's own value was read, and no other party's.
	if len(decrypter.got) != 1 || decrypter.got[0] != "identity for:"+ownOIN {
		t.Errorf("decrypted %v, want this source's value alone", decrypter.got)
	}

	var forwarded struct {
		Query         string         `json:"query"`
		Variables     map[string]any `json:"variables"`
		OperationName string         `json:"operationName"`
	}
	if err := json.Unmarshal(result.Body, &forwarded); err != nil {
		t.Fatalf("forwarded body: %v", err)
	}
	if forwarded.Variables["bsn"] != bsn {
		t.Errorf("variables = %v, want the BSN in place of the placeholder", forwarded.Variables)
	}
	// Everything else in the request is left as it came.
	if forwarded.Variables["vboId"] != "0632010000099412" || forwarded.OperationName != "Eigendom" ||
		!strings.Contains(forwarded.Query, "vbo(bsn: $bsn, vboId: $vboId)") {
		t.Errorf("the rest of the request changed: %s", result.Body)
	}
}

// A value made for another party's OIN is not accepted, whether the token
// simply has none for this source or files another party's value under it.
func TestAValueForAnotherPartyIsNotAccepted(t *testing.T) {
	t.Run("the token has no value for this source", func(t *testing.T) {
		decrypter := &fakeDecrypter{}
		tok := token(t, map[string]any{otherOIN: valuesFor(otherOIN)})

		_, err := substituter(decrypter).Apply(context.Background(), []byte(query), tok)
		if !errors.Is(err, ErrNoValue) {
			t.Fatalf("err = %v, want ErrNoValue", err)
		}
		if len(decrypter.got) != 0 {
			t.Errorf("another party's value was handed to the decryption component: %v", decrypter.got)
		}
	})

	t.Run("the value names another recipient than the token says", func(t *testing.T) {
		tok := token(t, map[string]any{ownOIN: valuesFor(otherOIN)})

		result, err := substituter(&fakeDecrypter{}).Apply(context.Background(), []byte(query), tok)
		if !errors.Is(err, ErrRecipient) {
			t.Fatalf("err = %v, want ErrRecipient", err)
		}
		if result.BSN != "" || strings.Contains(string(result.Body), bsn) {
			t.Errorf("a BSN from another party's value was used: %+v", result)
		}
	})
}

// Under a consent the subject comes from the token alone. The PDP denies a
// literal value, so one that arrives here did not come through it.
func TestALiteralSubjectIsRefused(t *testing.T) {
	decrypter := &fakeDecrypter{}
	tok := token(t, map[string]any{ownOIN: valuesFor(ownOIN)})

	for name, body := range map[string]string{
		"a BSN":                   `{"query":"q","variables":{"bsn":"999991772"}}`,
		"not a string":            `{"query":"q","variables":{"bsn":999991772}}`,
		"empty":                   `{"query":"q","variables":{"bsn":""}}`,
		"the retired placeholder": `{"query":"q","variables":{"bsn":"consent:subject"}}`,
	} {
		if _, err := substituter(decrypter).Apply(context.Background(), []byte(body), tok); !errors.Is(err, ErrLiteralSubject) {
			t.Errorf("%s: err = %v, want ErrLiteralSubject", name, err)
		}
	}
	if len(decrypter.got) != 0 {
		t.Errorf("decrypted %v for requests that were refused", decrypter.got)
	}
}

// A request that names no subject needs no identity, so none is decrypted.
func TestARequestWithoutASubjectIsLeftAlone(t *testing.T) {
	decrypter := &fakeDecrypter{}
	tok := token(t, map[string]any{ownOIN: valuesFor(ownOIN)})

	for _, body := range []string{
		`{"query":"{__schema{types{name}}}"}`,
		`{"query":"q","variables":null}`,
		`{"query":"q","variables":{"vboId":"0632010000099412"}}`,
	} {
		result, err := substituter(decrypter).Apply(context.Background(), []byte(body), tok)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if string(result.Body) != body || result.BSN != "" {
			t.Errorf("%s: result = %s, BSN %q; want the request as it came", body, result.Body, result.BSN)
		}
	}
	if len(decrypter.got) != 0 {
		t.Errorf("decrypted %v without a subject to fill in", decrypter.got)
	}
}

// A party not on the BSN authorisation list has a pseudonym and no identity.
// A request for its identity fails here, and the pseudonym is not read in its
// place.
func TestAnIdentityRequestForAPartyWithoutOneIsRefused(t *testing.T) {
	decrypter := &fakeDecrypter{}
	tok := token(t, map[string]any{ownOIN: pseudonymOnlyFor(ownOIN)})

	if _, err := substituter(decrypter).Apply(context.Background(), []byte(query), tok); !errors.Is(err, ErrNoValue) {
		t.Fatalf("err = %v, want ErrNoValue", err)
	}
	if len(decrypter.got) != 0 {
		t.Errorf("decrypted %v for a request that asked for a missing identity", decrypter.got)
	}
}

// The pseudonym placeholder becomes this source's own pseudonym of the
// citizen, read from the pseudonym the token carries for this source. The
// identity is not read: the request did not ask for it.
func TestThePseudonymPlaceholderBecomesTheSourcesPseudonym(t *testing.T) {
	for name, values := range map[string]map[string]any{
		"a party on the list":     valuesFor(ownOIN),
		"a party not on the list": pseudonymOnlyFor(ownOIN),
	} {
		decrypter := &fakeDecrypter{}
		tok := token(t, map[string]any{ownOIN: values, otherOIN: valuesFor(otherOIN)})
		body := strings.Replace(query, IdentityPlaceholder, PseudonymPlaceholder, 1)

		result, err := substituter(decrypter).Apply(context.Background(), []byte(body), tok)
		if err != nil {
			t.Fatalf("%s: Apply: %v", name, err)
		}
		if result.Pseudonym != pseudonym || result.BSN != "" {
			t.Errorf("%s: result = %+v, want the pseudonym and no BSN", name, result)
		}
		if len(decrypter.got) != 1 || decrypter.got[0] != "pseudonym for:"+ownOIN {
			t.Errorf("%s: decrypted %v, want this source's pseudonym alone", name, decrypter.got)
		}
		var forwarded struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(result.Body, &forwarded); err != nil {
			t.Fatalf("%s: forwarded body: %v", name, err)
		}
		if forwarded.Variables["bsn"] != pseudonym || forwarded.Variables["vboId"] != "0632010000099412" {
			t.Errorf("%s: variables = %v, want the pseudonym in place of the placeholder", name, forwarded.Variables)
		}
	}
}

// A pseudonym made for another party is not accepted either, and a token
// without a pseudonym for this source has nothing to read.
func TestAPseudonymForAnotherPartyIsNotAccepted(t *testing.T) {
	body := strings.Replace(query, IdentityPlaceholder, PseudonymPlaceholder, 1)

	_, err := substituter(&fakeDecrypter{}).Apply(context.Background(), []byte(body), token(t, map[string]any{ownOIN: pseudonymOnlyFor(otherOIN)}))
	if !errors.Is(err, ErrRecipient) {
		t.Errorf("another recipient: err = %v, want ErrRecipient", err)
	}
	_, err = substituter(&fakeDecrypter{}).Apply(context.Background(), []byte(body), token(t, map[string]any{otherOIN: valuesFor(otherOIN)}))
	if !errors.Is(err, ErrNoValue) {
		t.Errorf("no value for this source: err = %v, want ErrNoValue", err)
	}
}

func TestAFailedDecryptionLeavesThePlaceholder(t *testing.T) {
	decrypter := &fakeDecrypter{err: errors.New("no matching key")}
	tok := token(t, map[string]any{ownOIN: valuesFor(ownOIN)})

	result, err := substituter(decrypter).Apply(context.Background(), []byte(query), tok)
	if err == nil {
		t.Fatal("want an error when the value cannot be read")
	}
	if string(result.Body) != query || result.BSN != "" {
		t.Errorf("result = %+v, want the request untouched", result)
	}
	// The consent is known even so, which is what a record of the failure
	// names the citizen by.
	if result.ConsentID != "c-1" {
		t.Errorf("consent id = %q, want c-1", result.ConsentID)
	}
}

func TestUnreadableInputIsRefused(t *testing.T) {
	tok := token(t, map[string]any{ownOIN: valuesFor(ownOIN)})

	if _, err := substituter(&fakeDecrypter{}).Apply(context.Background(), []byte(query), "not-a-token"); !errors.Is(err, ErrToken) {
		t.Errorf("token: err = %v, want ErrToken", err)
	}
	if _, err := substituter(&fakeDecrypter{}).Apply(context.Background(), []byte(`not json`), tok); !errors.Is(err, ErrBody) {
		t.Errorf("body: err = %v, want ErrBody", err)
	}
}
