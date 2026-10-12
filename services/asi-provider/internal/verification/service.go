// Package verification is the core of the ASIP: the use cases Verify
// (ETSI TS 119 478 clause 6.1.1, I2 Verify) and Retrieve (clause 6.1.2,
// I3 Retrieve) against an authentic source. It knows nothing of HTTP, tokens
// or seals; the user is identified by the BSN the caller has established.
package verification

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Source is the authentic source. Value returns the stored value of an
// attribute for a person, as the JSON object the catalogue describes, and
// false when the source holds none.
type Source interface {
	Value(bsn, name string) (json.RawMessage, bool)
}

var (
	// ErrMandate: requests on behalf of another person are not supported
	// (REQ-ASIP-6.1.1.2-12, 501).
	ErrMandate = errors.New("requests on behalf of another person (mandate) are not supported")
)

// MalformedError is a request the use case cannot process: 400.
type MalformedError struct{ Reason string }

func (e *MalformedError) Error() string { return e.Reason }

// UnknownAttributeError is an attribute identifier that is not in the
// catalogue: 404.
type UnknownAttributeError struct{ Identifier string }

func (e *UnknownAttributeError) Error() string {
	return "unknown attribute identifier: " + e.Identifier
}

// Attribute is an attribute with its value.
type Attribute struct {
	Identifier string
	Value      json.RawMessage
}

// Fragment is a part of an attribute, selected by a JSONPath location.
type Fragment struct {
	Identifier string
	Location   string
	Value      json.RawMessage
}

// VerifyRequest is what a QTSP asks to verify. Mandate is true when the
// request is made on behalf of another person.
type VerifyRequest struct {
	Attributes []Attribute
	Fragments  []Fragment
	Mandate    bool
}

// Result is the verification result for one attribute or fragment.
type Result struct {
	Identifier string
	Result     string
}

// VerifyResult holds a result per requested attribute and fragment, in the
// order of the request.
type VerifyResult struct {
	Attributes []Result
	Fragments  []Result
}

// Service verifies and retrieves attributes against a source.
type Service struct {
	source    Source
	catalogue Catalogue
}

func NewService(source Source, catalogue Catalogue) *Service {
	return &Service{source: source, catalogue: catalogue}
}

// Verify compares the claimed values with the source for the person with
// bsn. It never returns the stored value: Match and MatchWithVariation carry
// no attributeValue in this mock.
func (s *Service) Verify(bsn string, req VerifyRequest) (VerifyResult, error) {
	if err := validateVerifyRequest(req); err != nil {
		return VerifyResult{}, err
	}
	if req.Mandate {
		return VerifyResult{}, ErrMandate
	}
	for _, id := range requestedIdentifiers(req) {
		if _, ok := s.catalogue.lookup(id); !ok {
			return VerifyResult{}, &UnknownAttributeError{Identifier: id}
		}
	}
	var res VerifyResult
	for _, a := range req.Attributes {
		res.Attributes = append(res.Attributes, Result{Identifier: a.Identifier, Result: s.verifyAttribute(bsn, a)})
	}
	for _, f := range req.Fragments {
		result, err := s.verifyFragment(bsn, f)
		if err != nil {
			return VerifyResult{}, err
		}
		res.Fragments = append(res.Fragments, Result{Identifier: f.Identifier, Result: result})
	}
	return res, nil
}

// Retrieve returns the stored values of the requested attributes for the
// person with bsn. An attribute this source is not the authentic source for,
// or holds no value for, is left out.
func (s *Service) Retrieve(bsn string, identifiers []string, mandate bool) ([]Attribute, error) {
	if len(identifiers) == 0 {
		return nil, &MalformedError{Reason: "attributeIdentifiers must contain at least one attribute"}
	}
	if mandate {
		return nil, ErrMandate
	}
	out := []Attribute{}
	for _, id := range identifiers {
		entry, ok := s.catalogue.lookup(id)
		if !ok {
			return nil, &UnknownAttributeError{Identifier: id}
		}
		if !entry.Ours {
			continue
		}
		if raw, ok := s.source.Value(bsn, entry.Name); ok {
			out = append(out, Attribute{Identifier: id, Value: raw})
		}
	}
	return out, nil
}

func (s *Service) verifyAttribute(bsn string, a Attribute) string {
	held, ok := s.heldValue(bsn, a.Identifier)
	if !ok {
		return ResultUnknown
	}
	var claimed any
	_ = json.Unmarshal(a.Value, &claimed)
	return compareValues(claimed, held)
}

// verifyFragment compares a fragment. The ETSI schema requires the fragment
// value to be an object; this mock reads it as {"<last member name>": value},
// for example location "$.city" with value {"city": "Rotterdam"}.
func (s *Service) verifyFragment(bsn string, f Fragment) (string, error) {
	held, ok := s.heldValue(bsn, f.Identifier)
	if !ok {
		return ResultUnknown, nil
	}
	selected, member, err := jsonPath(held, f.Location)
	if errors.Is(err, errPathEmpty) {
		return ResultUnknown, nil
	}
	if err != nil {
		return "", &MalformedError{Reason: err.Error()}
	}
	var claimed map[string]any
	_ = json.Unmarshal(f.Value, &claimed)
	if member == "" {
		return "", &MalformedError{Reason: "a fragment location must end in a member name"}
	}
	value, ok := claimed[member]
	if !ok || len(claimed) != 1 {
		return "", &MalformedError{Reason: `fragment value must be an object with the single member "` + member + `"`}
	}
	return compareValues(value, selected), nil
}

// heldValue returns the decoded value the source holds, and false when there
// is no authentic source data for it: the attribute is not ours, or the source
// holds no value for this person. Both yield Unknown
// (ETSI TS 119 478 REQ-ASIP-6.1.1.2-04).
func (s *Service) heldValue(bsn, id string) (any, bool) {
	entry, _ := s.catalogue.lookup(id)
	if !entry.Ours {
		return nil, false
	}
	raw, ok := s.source.Value(bsn, entry.Name)
	if !ok {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	return v, true
}

// validateVerifyRequest enforces the required members of the ETSI schema and
// REQ-ASIP-6.1.1.1-05 (attributes or attributeFragments, or both).
func validateVerifyRequest(req VerifyRequest) error {
	if len(req.Attributes) == 0 && len(req.Fragments) == 0 {
		return &MalformedError{Reason: "verifyRequest must contain attributes or attributeFragments"}
	}
	for _, a := range req.Attributes {
		if a.Identifier == "" {
			return &MalformedError{Reason: "attribute without attributeIdentifier"}
		}
		if !isJSONObject(a.Value) {
			return &MalformedError{Reason: "attributeValue of " + a.Identifier + " must be a JSON object"}
		}
	}
	for _, f := range req.Fragments {
		if f.Identifier == "" || f.Location == "" {
			return &MalformedError{Reason: "attributeFragment requires attributeIdentifier and location"}
		}
		if !isJSONObject(f.Value) {
			return &MalformedError{Reason: "value of fragment " + f.Location + " must be a JSON object"}
		}
	}
	return nil
}

func requestedIdentifiers(req VerifyRequest) []string {
	var ids []string
	for _, a := range req.Attributes {
		ids = append(ids, a.Identifier)
	}
	for _, f := range req.Fragments {
		ids = append(ids, f.Identifier)
	}
	return ids
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var m map[string]any
	return json.Unmarshal(trimmed, &m) == nil
}
