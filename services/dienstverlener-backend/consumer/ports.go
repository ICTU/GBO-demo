package consumer

import (
	"context"
	"encoding/json"
	"time"
)

// The driven ports, declared here at their consumer. Each is implemented by one
// adapter package (outway, logbook, devportal) and by in-memory fakes in the
// tests.

// Question is one request to the source: the GraphQL query and what travels
// with it.
type Question struct {
	Query     string
	Variables map[string]any
	// Scope is the requested scope. It is untrusted context for the PDP; the
	// consent token is what puts the request under the consent regime.
	Scope        string
	ConsentToken string
	// TransactionID is the identifier that travels through FSC and ends up as
	// the trace of the PDP's decision log.
	TransactionID string
	// Parent is where the source's records hang in the logbook chain. Zero
	// when this consumer writes no records.
	Parent Position
}

// Answer is what came back. A refusal is an answer too: the source's side
// (the PDP, or the source itself) said no, and Reason says why in its own
// words.
type Answer struct {
	Allowed bool
	// Data is the source's GraphQL response, passed on untouched.
	Data   json.RawMessage
	Reason string
}

// Source is the consumer's one data source, seen from the core. An error
// means the question did not reach the source's side at all.
type Source interface {
	Ask(ctx context.Context, q Question) (Answer, error)
}

// Position is the identity of one record in the logbook chain.
type Position struct {
	TraceID string
	SpanID  string
}

// Processing is one call to the source, as the consumer's own Logboek
// Dataverwerkingen records it. The call happened whatever the source answered,
// so a refused or failed call is a processing too.
type Processing struct {
	At Position
	// Activity and Name come from the Kind: which of the consumer's
	// verwerkingsactiviteiten the call is.
	Activity string
	Name     string
	// Subject is the citizen as the consent names them: a PI, never a BSN.
	Subject string
	// Scope names the source, so the logbook can point to where the source
	// logs its half.
	Scope  string
	Start  time.Time
	End    time.Time
	Failed bool
}

// Logbook is the consumer's Logboek Dataverwerkingen. Begin mints the record's
// identity before the call, so the source can hang its own records under it;
// Record must be durable before it returns nil. A nil Logbook means the
// consumer writes no records.
type Logbook interface {
	Begin(ctx context.Context) Position
	Record(ctx context.Context, p Processing) error
}

// Run is one question that reached the source, for the dev-portal timeline.
type Run struct {
	Request Request
	Result  Result
}

// History is the dev-portal timeline. It is best-effort: Observe returns
// nothing, so it cannot fail a question. A nil History reports nothing.
type History interface {
	Observe(ctx context.Context, run Run)
}
