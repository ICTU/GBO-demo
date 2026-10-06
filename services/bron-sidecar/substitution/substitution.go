// Package substitution is the core of the sidecar's one use case under a
// consent: putting the citizen of that consent into a request that names them
// with a placeholder.
//
// A consent-based request carries no identifier of the citizen. The consent
// token carries encrypted values per party, and the query names its subject
// with a placeholder for the form the source's API takes: IdentityPlaceholder
// for the BSN, PseudonymPlaceholder for the source's own pseudonym. The PDP
// decides on the request as it was sent. Only after its allow does the request
// reach this code, which takes the value made for this source out of the
// token, has the source's own decryption component read it, and replaces the
// placeholder with the BSN.
//
// The token carries no pseudonyms yet, so a request for one is refused as a
// token without a value for this source.
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

// identifierIdentity is BSNk's name for a value that decrypts to the BSN.
const identifierIdentity = "Identity"

// Identity is what the source reads from the value made for it.
type Identity struct {
	BSN string
	// Recipient is the OIN of the party the value was made for, as the value
	// itself says and BSNk signed.
	Recipient string
}

// Decrypter is the source's own decryption component seen from the core. It
// reads a value with the source's keys and without calling BSNk.
type Decrypter interface {
	Identity(ctx context.Context, value string) (Identity, error)
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
	// BSN is the citizen the request is now about. Empty when the request
	// named no subject, in which case nothing was decrypted.
	BSN string
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
	ErrRecipient = errors.New("the encrypted identity was made for another party")
	// ErrBody means the request body is not a GraphQL request.
	ErrBody = errors.New("the request body is not a GraphQL request")
)

// claims is what the substitution reads from the consent token. The token is
// not verified here: the PDP verified it on this same request, and this code
// runs only after its allow.
type claims struct {
	ConsentID        string `json:"consent_id"`
	EncryptedSubject map[string]struct {
		IdentifierType string `json:"identifier_type"`
		Value          string `json:"value"`
	} `json:"encrypted_subject"`
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

// Apply replaces the placeholder in body with the BSN of the citizen the
// consent token is about.
//
// A request that names no subject is returned as it came and nothing is
// decrypted. A request that names its subject with anything but a
// placeholder is refused: the PDP denies those, so one that arrives here did
// not come through it. A request for the pseudonym is refused with
// ErrNoValue, since the token carries none.
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

	var placeholders []string
	for _, name := range s.Variables {
		raw, present := variables[name]
		if !present {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return result, fmt.Errorf("%w: variable %q", ErrLiteralSubject, name)
		}
		switch value {
		case IdentityPlaceholder:
			placeholders = append(placeholders, name)
		case PseudonymPlaceholder:
			return result, fmt.Errorf("%w: variable %q asks for a pseudonym", ErrNoValue, name)
		default:
			return result, fmt.Errorf("%w: variable %q", ErrLiteralSubject, name)
		}
	}
	if len(placeholders) == 0 {
		return result, nil
	}

	encrypted, present := token.EncryptedSubject[s.OwnOIN]
	if !present {
		return result, fmt.Errorf("%w: no encrypted identity", ErrNoValue)
	}
	if encrypted.IdentifierType != identifierIdentity {
		return result, fmt.Errorf("%w: the value for this source is a %q", ErrNoValue, encrypted.IdentifierType)
	}
	identity, err := s.Decrypter.Identity(ctx, encrypted.Value)
	if err != nil {
		return result, fmt.Errorf("decrypt the identity: %w", err)
	}
	// The token's key says whom a value is for, but only the value itself,
	// which BSNk signed, is evidence of it.
	if identity.Recipient != s.OwnOIN {
		return result, fmt.Errorf("%w: %s", ErrRecipient, identity.Recipient)
	}
	if identity.BSN == "" {
		return result, errors.New("decrypt the identity: the decryption component returned no BSN")
	}

	bsn, _ := json.Marshal(identity.BSN)
	for _, name := range placeholders {
		variables[name] = bsn
	}
	request["variables"], _ = json.Marshal(variables)
	substituted, err := json.Marshal(request)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrBody, err)
	}
	result.Body, result.BSN = substituted, identity.BSN
	return result, nil
}
