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
	"fmt"

	asimapping "gbo-demo/asi-mapping"
)

// Source is the authentic source of a data service, known by its
// description: the asi capability it publishes in its source metadata.
//
// Description returns the current description, ErrSourceUnavailable when
// there is none. Ask asks the source once, with document, a query derived
// from that description, for the person with bsn, and returns its answer at
// the description's result_pointer: nil for a person the source does not
// know. identifiers name the attributes asked, for tracing. An error means
// the source gave no answer: ErrSourceUnavailable when it could not be
// reached, any other error when it refused or answered in a way the ASIP
// cannot read.
type Source interface {
	Description() (*asimapping.Capability, error)
	Ask(ctx context.Context, bsn string, asi *asimapping.Capability, document string, identifiers []string) (any, error)
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

// UnknownAttributeError is an attribute identifier the source does not
// describe: 404.
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
// order of the request, and the legal name of the authentic source in the
// description the source was asked by.
type VerifyResult struct {
	Attributes      []Result
	Fragments       []Result
	AuthenticSource string
}

// RetrieveResult holds the stored values of the requested attributes, and
// the legal name of the authentic source in the description the source was
// asked by.
type RetrieveResult struct {
	Attributes      []Attribute
	AuthenticSource string
}

// Service verifies and retrieves attributes against a source. catalogue
// holds GBO's value schema per attribute identifier: the shape the source's
// answer is built into, and which strings compare with variation.
type Service struct {
	source    Source
	catalogue asimapping.Catalogue
}

func NewService(source Source, catalogue asimapping.Catalogue) *Service {
	return &Service{source: source, catalogue: catalogue}
}

// Description returns the source's current description: ErrSourceUnavailable
// when there is none.
func (s *Service) Description() (*asimapping.Capability, error) {
	return s.source.Description()
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
	asi, err := s.describe(requestedIdentifiers(req))
	if err != nil {
		return VerifyResult{}, err
	}
	// A fragment asks only its own member. A member the attribute does not
	// have, or a const, is no source data: Unknown, and nothing to ask.
	var requests []asimapping.Request
	for _, a := range req.Attributes {
		requests = append(requests, asimapping.Request{Identifier: a.Identifier})
	}
	members := make([]string, len(req.Fragments))
	for i, f := range req.Fragments {
		path, err := parsePath(f.Location)
		if err != nil {
			return VerifyResult{}, &MalformedError{Reason: err.Error()}
		}
		if path[0].index {
			return VerifyResult{}, &MalformedError{Reason: "a fragment location must start with a member name"}
		}
		field, ok := attributeOf(asi, f.Identifier).Fields[path[0].member]
		if !ok || field.Const != nil {
			continue
		}
		members[i] = path[0].member
		requests = append(requests, asimapping.Request{Identifier: f.Identifier, Members: []string{members[i]}})
	}
	result, err := s.ask(ctx, bsn, asi, requests)
	if err != nil {
		return VerifyResult{}, err
	}
	res := VerifyResult{AuthenticSource: asi.AuthenticSource.LegalName}
	for _, a := range req.Attributes {
		value, held, err := s.build(asi, result, a.Identifier, nil)
		if err != nil {
			return VerifyResult{}, err
		}
		outcome := ResultUnknown
		if held {
			var claimed any
			_ = json.Unmarshal(a.Value, &claimed)
			outcome = compareValues(claimed, value, s.catalogue[a.Identifier])
		}
		res.Attributes = append(res.Attributes, Result{Identifier: a.Identifier, Result: outcome})
	}
	for i, f := range req.Fragments {
		outcome := ResultUnknown
		if members[i] != "" {
			value, held, err := s.build(asi, result, f.Identifier, []string{members[i]})
			if err != nil {
				return VerifyResult{}, err
			}
			if held {
				if outcome, err = verifyFragment(value, s.catalogue[f.Identifier], f); err != nil {
					return VerifyResult{}, err
				}
			}
		}
		res.Fragments = append(res.Fragments, Result{Identifier: f.Identifier, Result: outcome})
	}
	return res, nil
}

// Retrieve returns the stored values of the requested attributes for the
// person with bsn. If the source holds no value for one of them, the request
// is not found as a whole: there is no partial result.
func (s *Service) Retrieve(ctx context.Context, bsn string, identifiers []string, mandate bool) (RetrieveResult, error) {
	if len(identifiers) == 0 {
		return RetrieveResult{}, &MalformedError{Reason: "attributeIdentifiers must contain at least one attribute"}
	}
	if mandate {
		return RetrieveResult{}, ErrMandate
	}
	asi, err := s.describe(identifiers)
	if err != nil {
		return RetrieveResult{}, err
	}
	requests := make([]asimapping.Request, len(identifiers))
	for i, id := range identifiers {
		requests[i] = asimapping.Request{Identifier: id}
	}
	result, err := s.ask(ctx, bsn, asi, requests)
	if err != nil {
		return RetrieveResult{}, err
	}
	out := RetrieveResult{Attributes: make([]Attribute, 0, len(identifiers)), AuthenticSource: asi.AuthenticSource.LegalName}
	for _, id := range identifiers {
		value, held, err := s.build(asi, result, id, nil)
		if err != nil {
			return RetrieveResult{}, err
		}
		if !held {
			return RetrieveResult{}, &NotHeldError{Identifier: id}
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return RetrieveResult{}, err
		}
		out.Attributes = append(out.Attributes, Attribute{Identifier: id, Value: raw})
	}
	return out, nil
}

// describe returns the source's current description, after checking that it
// describes every attribute in ids.
func (s *Service) describe(ids []string) (*asimapping.Capability, error) {
	asi, err := s.source.Description()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if attributeOf(asi, id).Identifier == "" {
			return nil, &UnknownAttributeError{Identifier: id}
		}
	}
	return asi, nil
}

// ask asks the source once for requests and returns its answer; without a
// field to ask there is no question, and no answer.
func (s *Service) ask(ctx context.Context, bsn string, asi *asimapping.Capability, requests []asimapping.Request) (any, error) {
	document, err := asi.Plan(requests)
	if err != nil {
		return nil, err
	}
	if document == "" {
		return nil, nil
	}
	var identifiers []string
	for _, r := range requests {
		identifiers = append(identifiers, r.Identifier)
	}
	return s.source.Ask(ctx, bsn, asi, document, identifiers)
}

// build makes the value of the attribute with id from the source's answer,
// or of its members only. An error is an answer of another shape than the
// description and the value schema expect (500), not a missing value.
func (s *Service) build(asi *asimapping.Capability, result any, id string, members []string) (map[string]any, bool, error) {
	schema, ok := s.catalogue[id]
	if !ok {
		return nil, false, fmt.Errorf("attribute %q is not in GBO's attribute catalogue", id)
	}
	return attributeOf(asi, id).Build(result, schema, members)
}

func attributeOf(asi *asimapping.Capability, id string) asimapping.Attribute {
	for _, attribute := range asi.Attributes {
		if attribute.Identifier == id {
			return attribute
		}
	}
	return asimapping.Attribute{}
}

// verifyFragment compares a fragment with value, the attribute built with
// the fragment's member. The ETSI schema requires the fragment value to be
// an object; this mock reads it as {"<last member name>": value}, for
// example location "$.city" with value {"city": "Rotterdam"}.
func verifyFragment(value map[string]any, schema *asimapping.ValueSchema, f Fragment) (string, error) {
	path, err := parsePath(f.Location)
	if err != nil {
		return "", &MalformedError{Reason: err.Error()}
	}
	selected, err := path.selectIn(value)
	if errors.Is(err, errPathEmpty) {
		return ResultUnknown, nil
	}
	if err != nil {
		return "", &MalformedError{Reason: err.Error()}
	}
	member := path.lastMember()
	var claimed map[string]any
	_ = json.Unmarshal(f.Value, &claimed)
	if member == "" {
		return "", &MalformedError{Reason: "a fragment location must end in a member name"}
	}
	claimedValue, ok := claimed[member]
	if !ok || len(claimed) != 1 {
		return "", &MalformedError{Reason: `fragment value must be an object with the single member "` + member + `"`}
	}
	return compareValues(claimedValue, selected, path.schemaIn(schema)), nil
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
