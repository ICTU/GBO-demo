// Package verification is the core of the ASIP: the use cases Verify
// (ETSI TS 119 478 clause 6.1.1, I2 Verify) and Retrieve (clause 6.1.2,
// I3 Retrieve) against an authentic source. It knows nothing of HTTP, tokens
// or seals; the user is identified by the BSN the caller has established.
package verification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
)

// Source is the authentic source. Values asks it once for the attributes
// with names for a person, and returns per name the stored value as the JSON
// object the catalogue describes; a name the source holds no value for is
// absent. An error means the source gave no answer: ErrSourceUnavailable
// when it could not be reached, any other error when it refused or answered
// in a way the ASIP cannot read.
type Source interface {
	Values(ctx context.Context, bsn string, names []string) (map[string]json.RawMessage, error)
}

var (
	// ErrMandate: requests on behalf of another person are not supported
	// (REQ-ASIP-6.1.1.2-12, 501).
	ErrMandate = errors.New("requests on behalf of another person (mandate) are not supported")

	// ErrSourceUnavailable: the authentic source could not be reached (503).
	// Not Unknown: that result means the source holds no data.
	ErrSourceUnavailable = errors.New("authentic source unavailable")
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

// NotHeldError is a requested attribute the source holds no value for, on
// retrieve: 404 (REQ-ASIP-6.1.2.2-06). Verify answers Unknown instead.
type NotHeldError struct{ Identifier string }

func (e *NotHeldError) Error() string {
	return "the authentic source holds no value for attribute " + e.Identifier
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
func (s *Service) Verify(ctx context.Context, bsn string, req VerifyRequest) (VerifyResult, error) {
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
	held, err := s.held(ctx, bsn, requestedIdentifiers(req))
	if err != nil {
		return VerifyResult{}, err
	}
	var res VerifyResult
	for _, a := range req.Attributes {
		res.Attributes = append(res.Attributes, Result{Identifier: a.Identifier, Result: verifyAttribute(held, a)})
	}
	for _, f := range req.Fragments {
		result, err := verifyFragment(held, f)
		if err != nil {
			return VerifyResult{}, err
		}
		res.Fragments = append(res.Fragments, Result{Identifier: f.Identifier, Result: result})
	}
	return res, nil
}

// Retrieve returns the stored values of the requested attributes for the
// person with bsn. If the source holds no value for one of them, the request
// is not found as a whole: there is no partial result.
func (s *Service) Retrieve(ctx context.Context, bsn string, identifiers []string, mandate bool) ([]Attribute, error) {
	if len(identifiers) == 0 {
		return nil, &MalformedError{Reason: "attributeIdentifiers must contain at least one attribute"}
	}
	if mandate {
		return nil, ErrMandate
	}
	for _, id := range identifiers {
		if _, ok := s.catalogue.lookup(id); !ok {
			return nil, &UnknownAttributeError{Identifier: id}
		}
	}
	held, err := s.held(ctx, bsn, identifiers)
	if err != nil {
		return nil, err
	}
	out := make([]Attribute, 0, len(identifiers))
	for _, id := range identifiers {
		v, ok := held[id]
		if !ok {
			return nil, &NotHeldError{Identifier: id}
		}
		out = append(out, Attribute{Identifier: id, Value: v.raw})
	}
	return out, nil
}

// heldValue is a value the source holds, as it came and decoded.
type heldValue struct {
	raw     json.RawMessage
	decoded any
}

// held asks the source once for the attributes with identifiers ids, which
// are in the catalogue, and returns the values it holds by identifier. An
// attribute without a value is absent: Unknown (ETSI TS 119 478
// REQ-ASIP-6.1.1.2-04).
func (s *Service) held(ctx context.Context, bsn string, ids []string) (map[string]heldValue, error) {
	var names []string
	for _, id := range ids {
		entry, _ := s.catalogue.lookup(id)
		if !slices.Contains(names, entry.Name) {
			names = append(names, entry.Name)
		}
	}
	values, err := s.source.Values(ctx, bsn, names)
	if err != nil {
		return nil, err
	}
	held := map[string]heldValue{}
	for _, id := range ids {
		entry, _ := s.catalogue.lookup(id)
		raw, ok := values[entry.Name]
		var v any
		if !ok || json.Unmarshal(raw, &v) != nil {
			continue
		}
		held[id] = heldValue{raw: raw, decoded: v}
	}
	return held, nil
}

func verifyAttribute(held map[string]heldValue, a Attribute) string {
	v, ok := held[a.Identifier]
	if !ok {
		return ResultUnknown
	}
	var claimed any
	_ = json.Unmarshal(a.Value, &claimed)
	return compareValues(claimed, v.decoded)
}

// verifyFragment compares a fragment. The ETSI schema requires the fragment
// value to be an object; this mock reads it as {"<last member name>": value},
// for example location "$.city" with value {"city": "Rotterdam"}.
func verifyFragment(held map[string]heldValue, f Fragment) (string, error) {
	v, ok := held[f.Identifier]
	if !ok {
		return ResultUnknown, nil
	}
	selected, member, err := jsonPath(v.decoded, f.Location)
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
