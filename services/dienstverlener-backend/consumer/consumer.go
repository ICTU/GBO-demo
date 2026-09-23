// Package consumer is the domain core of the consumer backend: asking a source
// a question under a citizen's consent, and recording that it asked. The same
// core serves Hypotheek-BV (income data from the Belastingdienst) and the
// Installatie Register (an ownership check at LVG); a Kind says which question.
//
// The package imports no transport library: nothing here may import net/http.
// The FSC Outway, the logbook and the dev-portal timeline sit behind the ports
// in ports.go; the HTTP API is a driving adapter in consumerhttp.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Consumer answers questions for one consumer. Source is required; Logbook
// and History may be nil.
type Consumer struct {
	Kind    Kind
	Source  Source
	Logbook Logbook
	History History
	// Now is time.Now when nil; injected by tests.
	Now func() time.Time
}

// Request is one question as the consumer's caller puts it.
type Request struct {
	ConsentToken string
	// ScopeID is sent as the requested scope; empty means the Kind's default.
	ScopeID        string
	Belastingjaren []int
	Fields         []string
	// VboID is the verblijfsobject the Installatie Register asks about.
	VboID string
	// FromDevPortal marks a run the developer portal drove. It is sent exactly
	// as asked, so a policy outcome shows unfiltered, and it is already on the
	// portal's timeline.
	FromDevPortal bool
	// TraceID and TransactionID identify the run; they are echoed in the
	// Result, and the transaction id travels through FSC.
	TraceID       string
	TransactionID string
}

// Outcome is how a question ended. The driving adapter picks the HTTP status.
type Outcome int

const (
	// Allowed: the source answered with data.
	Allowed Outcome = iota + 1
	// Denied: the source's side refused, by policy or by failing.
	Denied
	// InvalidToken: the consent token cannot even be read to build the
	// question, so nothing was asked.
	InvalidToken
	// Unlogged: the call could not be recorded, so its answer is withheld.
	Unlogged
	// Unreachable: the question did not reach the source's side.
	Unreachable
)

// Result is the answer to one question. Its JSON is the consumer's API
// response, which the dev-portal timeline stores verbatim.
type Result struct {
	Outcome Outcome `json:"-"`
	// ConsentID is the consent the question was asked under, for logs; the
	// caller already has the token.
	ConsentID string          `json:"-"`
	Data      json.RawMessage `json:"data,omitempty"`
	// Reason is technical text for logs and the developer portal.
	Reason string `json:"reason,omitempty"`
	// DenialCode is what a citizen UI renders; see denial.go.
	DenialCode string `json:"denial_code,omitempty"`
	TraceID    string `json:"trace_id"`
	// TransactionID is the identifier that travelled through the FSC chain
	// (Fsc-Transaction-Id → X-Request-Id → the PDP's reconstructed trace, and
	// so the OpenFTV decision log's input.context.trace_id). It equals TraceID
	// only when no caller supplied a traceparent; the dev-portal always does,
	// so decision-log lookups must use this one.
	TransactionID string `json:"fsc_transaction_id,omitempty"`
	// DeniedYears lists the requested belastingjaren the consent does not
	// cover and that were therefore not asked; a frontend greys them out.
	DeniedYears []int `json:"denied_years,omitempty"`
}

// Allowed reports whether the source answered with data.
func (r Result) Allowed() bool { return r.Outcome == Allowed }

// MarshalJSON adds `allowed`, the one field every caller reads first.
func (r Result) MarshalJSON() ([]byte, error) {
	type fields Result
	return json.Marshal(struct {
		Allowed bool `json:"allowed"`
		fields
	}{r.Allowed(), fields(r)})
}

// Ask puts the request to the source. An error is the caller's: the request
// cannot be asked at all. Every other way a question ends is a Result.
func (c *Consumer) Ask(ctx context.Context, req Request) (Result, error) {
	if req.ConsentToken == "" {
		return Result{}, errors.New("consent_token is required")
	}
	if req.ScopeID == "" {
		req.ScopeID = c.Kind.DefaultScope
	}

	// The token is read only to build the question. The PDP, not this
	// consumer, verifies it.
	claims, err := readConsentToken(req.ConsentToken)
	if err != nil {
		return Result{
			Outcome:    InvalidToken,
			Reason:     "invalid_consent_token: " + err.Error(),
			DenialCode: DenialCodeUnavailable,
			TraceID:    req.TraceID,
		}, nil
	}
	built, err := c.Kind.build(req, claims)
	if err != nil {
		return Result{}, err
	}

	var at Position
	if c.Logbook != nil {
		at = c.Logbook.Begin(ctx)
	}
	start := c.now()
	answer, callErr := c.Source.Ask(ctx, Question{
		Query:         built.query,
		Variables:     built.variables,
		Scope:         req.ScopeID,
		ConsentToken:  req.ConsentToken,
		TransactionID: req.TransactionID,
		Parent:        at,
	})

	result := Result{ConsentID: claims.ConsentID, TraceID: req.TraceID, TransactionID: req.TransactionID}
	// Logged before the answer is used: a call that cannot be made durable
	// does not hand out what it fetched.
	if c.Logbook != nil {
		err := c.Logbook.Record(ctx, Processing{
			At:       at,
			Activity: c.Kind.Activity,
			Name:     c.Kind.RecordName,
			Subject:  claims.PI,
			Scope:    req.ScopeID,
			Start:    start,
			End:      c.now(),
			Failed:   callErr != nil || !answer.Allowed,
		})
		if err != nil {
			result.Outcome = Unlogged
			result.Reason = "the query could not be logged; withholding the answer"
			result.DenialCode = DenialCodeUnavailable
			return result, nil
		}
	}
	if callErr != nil {
		result.Outcome = Unreachable
		result.Reason = callErr.Error()
		result.DenialCode = DenialCodeUnavailable
		return result, nil
	}

	if answer.Allowed {
		result.Outcome = Allowed
		result.Data = answer.Data
		result.DeniedYears = built.deniedYears
	} else {
		result.Outcome = Denied
		result.Reason = answer.Reason
		result.DenialCode = denialCodeFor(answer.Reason)
	}
	if c.History != nil {
		c.History.Observe(ctx, Run{Request: req, Result: result})
	}
	return result, nil
}

func (c *Consumer) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
