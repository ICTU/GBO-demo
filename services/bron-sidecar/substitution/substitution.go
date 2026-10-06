// Package substitution is the core of the sidecar's one use case under a
// consent: putting the citizen of that consent into a request that names them
// with a placeholder.
//
// A consent-based request carries no identifier of the citizen. The consent
// token carries encrypted values per party, and the query names its subject
// with a placeholder for the form the source's API takes: IdentityPlaceholder
// for the BSN, PseudonymPlaceholder for the source's own pseudonym. The PDP
// decides on the request as it was sent. Only after its allow does the request
// reach this code, which takes the value of that form made for this source out
// of the token, has the source's own decryption component read it, and
// replaces the placeholder with what it reads.
//
// The package imports no transport library. The decryption component is a
// port, Decrypter, so that the substitution can move to wherever "after the
// allow" is enforced without taking an HTTP client along.
package substitution

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The placeholders a consent-based query names its subject with.
const (
	// IdentityPlaceholder asks for the BSN of the citizen of the consent.
	IdentityPlaceholder = "consent:identity"
	// PseudonymPlaceholder asks for this source's own pseudonym of the
	// citizen of the consent.
	PseudonymPlaceholder = "consent:pseudonym"
)

// Identity is what the source reads from the identity made for it.
type Identity struct {
	BSN string
	// Recipient is the OIN of the party the value was made for, as the value
	// itself says and BSNk signed.
	Recipient string
}

// Pseudonym is what the source reads from the pseudonym made for it.
type Pseudonym struct {
	// Value is the source's own pseudonym of the citizen, in the form the
	// source stores it.
	Value string
	// Recipient is the OIN of the party the value was made for, as the value
	// itself says and BSNk signed.
	Recipient string
}

// Decrypter is the source's own decryption component seen from the core. It
// reads a value with the source's keys and without calling BSNk.
type Decrypter interface {
	Identity(ctx context.Context, value string) (Identity, error)
	Pseudonym(ctx context.Context, value string) (Pseudonym, error)
}

// Substituter replaces the placeholder in the subject variables of a request.
type Substituter struct {
	// OwnOIN is the OIN of this source: the party whose value is taken from
	// the token, and the only recipient a value is accepted for.
	OwnOIN string
	// Variables are the names of the GraphQL variables that name the subject.
	Variables []string
	Decrypter Decrypter
}

// Result is a request ready to be forwarded.
type Result struct {
	// Body is the request body, with the placeholder replaced where the
	// request named its subject with it.
	Body []byte
	// BSN is the citizen the request is now about, when it asked for the
	// identity. Pseudonym is this source's pseudonym of the citizen, when it
	// asked for the pseudonym. Both are empty when the request named no
	// subject, in which case nothing was decrypted.
	BSN       string
	Pseudonym string
	// ConsentID is the consent the token is for. It is filled as soon as the
	// token could be read, also when Apply fails after that.
	ConsentID string
}

// The ways a substitution is refused. Each leaves the request unforwarded.
var (
	// ErrToken means the consent token could not be read.
	ErrToken = errors.New("the consent token cannot be read")
	// ErrNoValue means the token carries no value for this source in the
	// form the request asks for.
	ErrNoValue = errors.New("the consent token carries no encrypted value for this source in the requested form")
	// ErrLiteralSubject means the request names its subject with a value of
	// its own. Under a consent the subject comes from the token alone.
	ErrLiteralSubject = errors.New("under a consent token the subject must be a placeholder")
	// ErrRecipient means the value turned out to be made for another party.
	ErrRecipient = errors.New("the encrypted value was made for another party")
	// ErrBody means the request body is not a GraphQL request.
	ErrBody = errors.New("the request body is not a GraphQL request")
)

// claims is what the substitution reads from the consent token. The token is
// not verified here: the PDP verified it on this same request, and this code
// runs only after its allow.
type claims struct {
	ConsentID        string `json:"consent_id"`
	EncryptedSubject map[string]struct {
		Identity  *encryptedValue `json:"identity"`
		Pseudonym *encryptedValue `json:"pseudonym"`
	} `json:"encrypted_subject"`
}

// encryptedValue is one value BSNk made for a party and a version of its keys.
type encryptedValue struct {
	KeySetVersion int    `json:"key_set_version"`
	Value         string `json:"value"`
}

func readToken(token string) (claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims{}, fmt.Errorf("%w: not a compact JWT", ErrToken)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, fmt.Errorf("%w: %v", ErrToken, err)
	}
	var c claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return claims{}, fmt.Errorf("%w: %v", ErrToken, err)
	}
	return c, nil
}

// Apply replaces each placeholder in body with what it asks for: the BSN for
// IdentityPlaceholder, this source's pseudonym of the citizen for
// PseudonymPlaceholder. Each is read from the value of that form the token
// carries for this source.
//
// A request that names no subject is returned as it came and nothing is
// decrypted. A request that names its subject with anything but a
// placeholder is refused: the PDP denies those, so one that arrives here did
// not come through it. A request for a form the token has no value of for
// this source is refused with ErrNoValue; a party not on the BSN
// authorisation list has no identity.
func (s Substituter) Apply(ctx context.Context, body []byte, consentToken string) (Result, error) {
	token, err := readToken(consentToken)
	if err != nil {
		return Result{}, err
	}
	result := Result{Body: body, ConsentID: token.ConsentID}

	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return result, fmt.Errorf("%w: %v", ErrBody, err)
	}
	var variables map[string]json.RawMessage
	if raw, present := request["variables"]; present && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &variables); err != nil {
			return result, fmt.Errorf("%w: variables: %v", ErrBody, err)
		}
	}

	// The subject variables, by the placeholder they name.
	asked := map[string][]string{}
	for _, name := range s.Variables {
		raw, present := variables[name]
		if !present {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || (value != IdentityPlaceholder && value != PseudonymPlaceholder) {
			return result, fmt.Errorf("%w: variable %q", ErrLiteralSubject, name)
		}
		asked[value] = append(asked[value], name)
	}
	if len(asked) == 0 {
		return result, nil
	}

	own := token.EncryptedSubject[s.OwnOIN]
	var bsn, pseudonym string
	if len(asked[IdentityPlaceholder]) > 0 {
		if bsn, err = s.identity(ctx, own.Identity); err != nil {
			return result, err
		}
	}
	if len(asked[PseudonymPlaceholder]) > 0 {
		if pseudonym, err = s.pseudonym(ctx, own.Pseudonym); err != nil {
			return result, err
		}
	}

	filled := map[string]string{IdentityPlaceholder: bsn, PseudonymPlaceholder: pseudonym}
	for placeholder, names := range asked {
		value, _ := json.Marshal(filled[placeholder])
		for _, name := range names {
			variables[name] = value
		}
	}
	request["variables"], _ = json.Marshal(variables)
	substituted, err := json.Marshal(request)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrBody, err)
	}
	result.Body, result.BSN, result.Pseudonym = substituted, bsn, pseudonym
	return result, nil
}

// identity reads the BSN from the identity the token carries for this
// source.
func (s Substituter) identity(ctx context.Context, encrypted *encryptedValue) (string, error) {
	if encrypted == nil || encrypted.Value == "" {
		return "", fmt.Errorf("%w: no encrypted identity", ErrNoValue)
	}
	identity, err := s.Decrypter.Identity(ctx, encrypted.Value)
	if err != nil {
		return "", fmt.Errorf("decrypt the identity: %w", err)
	}
	// The token's key says whom a value is for, but only the value itself,
	// which BSNk signed, is evidence of it.
	if identity.Recipient != s.OwnOIN {
		return "", fmt.Errorf("%w: %s", ErrRecipient, identity.Recipient)
	}
	if identity.BSN == "" {
		return "", errors.New("decrypt the identity: the decryption component returned no BSN")
	}
	return identity.BSN, nil
}

// pseudonym reads this source's pseudonym of the citizen from the pseudonym
// the token carries for this source.
func (s Substituter) pseudonym(ctx context.Context, encrypted *encryptedValue) (string, error) {
	if encrypted == nil || encrypted.Value == "" {
		return "", fmt.Errorf("%w: no encrypted pseudonym", ErrNoValue)
	}
	pseudonym, err := s.Decrypter.Pseudonym(ctx, encrypted.Value)
	if err != nil {
		return "", fmt.Errorf("decrypt the pseudonym: %w", err)
	}
	if pseudonym.Recipient != s.OwnOIN {
		return "", fmt.Errorf("%w: %s", ErrRecipient, pseudonym.Recipient)
	}
	if pseudonym.Value == "" {
		return "", errors.New("decrypt the pseudonym: the decryption component returned no pseudonym")
	}
	return pseudonym.Value, nil
}
