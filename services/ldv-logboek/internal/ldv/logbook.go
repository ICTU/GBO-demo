package ldv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Repository is the port the logbook needs from durable storage. It is
// deliberately narrow: append one record, and confirm it landed.
//
// Append MUST have committed by the time it returns nil. That is the whole
// contract — LDV requires the logbook to confirm every write, and a producer
// that gets a confirmation is entitled to conclude the record survives a
// restart. Nothing here may buffer, batch or sample.
type Repository interface {
	// Append stores the record. It returns ErrDuplicateRecord when this
	// (trace_id, span_id) pair is already present.
	Append(ctx context.Context, stored Stored) error
	// Query answers a read. The query has already been validated and capped.
	Query(ctx context.Context, query Query) ([]Stored, error)
}

// ErrDuplicateRecord is returned when a record with the same (trace_id,
// span_id) is already stored. A producer that retries after a timeout will
// hit this; the logbook treats it as success-on-replay rather than as a
// second Dataverwerking, because span ids identify the operation, not the
// attempt.
var ErrDuplicateRecord = errors.New("record already stored")

// Clock is the logbook's own notion of now, injected so tests get a fixed
// ReceivedAt.
type Clock func() time.Time

// Logbook is the single use case this service offers: accept a Dataverwerking
// record from a component of this Verantwoordelijke, judge it, store it,
// confirm it.
type Logbook struct {
	repository Repository
	register   *Register
	now        Clock
}

// NewLogbook wires the use case. A nil clock means the wall clock.
func NewLogbook(repository Repository, register *Register, now Clock) (*Logbook, error) {
	if repository == nil {
		return nil, fmt.Errorf("logbook needs a repository")
	}
	if register == nil {
		return nil, fmt.Errorf("logbook needs a verwerkingsactiviteiten register")
	}
	if now == nil {
		now = time.Now
	}
	return &Logbook{repository: repository, register: register, now: now}, nil
}

// Register exposes the register so the HTTP adapter can serve it at the URIs
// the records point to. Records reference it; a reference nobody can resolve
// is not a reference.
func (l *Logbook) Register() *Register { return l.register }

// Confirmation is what a producer gets back once the record is durable.
// Duplicate says the record was already there, so the producer knows its
// retry did not create a second Dataverwerking.
type Confirmation struct {
	TraceID    string    `json:"trace_id"`
	SpanID     string    `json:"span_id"`
	ReceivedAt time.Time `json:"received_at"`
	Duplicate  bool      `json:"duplicate"`
}

// Write validates and stores one record. It returns only after the write is
// durable: the confirmation is the producer's evidence, and a producer that
// treats LDV as a hard requirement is expected to fail its own request when
// this call does not succeed.
func (l *Logbook) Write(ctx context.Context, record Record) (Confirmation, error) {
	if err := Validate(record, l.resolves); err != nil {
		return Confirmation{}, err
	}
	stored := Stored{Record: record, ReceivedAt: l.now().UTC()}
	switch err := l.repository.Append(ctx, stored); {
	case err == nil:
		return Confirmation{TraceID: record.TraceID, SpanID: record.SpanID, ReceivedAt: stored.ReceivedAt}, nil
	case errors.Is(err, ErrDuplicateRecord):
		return Confirmation{TraceID: record.TraceID, SpanID: record.SpanID, ReceivedAt: stored.ReceivedAt, Duplicate: true}, nil
	default:
		return Confirmation{}, fmt.Errorf("append record: %w", err)
	}
}

func (l *Logbook) resolves(reference string) bool {
	_, ok := l.register.Resolve(reference)
	return ok
}

// Query selects records. Exactly one of the three axes must be set — LDV's
// read extension is deliberately not a general query language, because a
// logbook you can browse freely is a logbook that has become a second copy of
// the data it describes.
type Query struct {
	TraceID              string
	ProcessingActivityID string
	DataSubjectID        string
	// DataSubjectIDType narrows a subject lookup to one pseudonym space. Two
	// Verantwoordelijken can name different people by the same string, so
	// without it a subject query is ambiguous.
	DataSubjectIDType string
	Limit             int
}

// ErrNoSelector is returned for a read that names none of the three axes.
var ErrNoSelector = errors.New("a read needs a traceID, a processingActivityID or a dataSubjectId")

// MaxReadLimit caps a single read. A cap rather than pagination: this is a
// demo of the read extension existing, not of reading a production logbook.
const MaxReadLimit = 500

// defaultReadLimit applies when the caller names none.
const defaultReadLimit = 100

// normalize validates the query and applies the cap.
func (q Query) normalize() (Query, error) {
	selectors := 0
	for _, selector := range []string{q.TraceID, q.ProcessingActivityID, q.DataSubjectID} {
		if strings.TrimSpace(selector) != "" {
			selectors++
		}
	}
	if selectors == 0 {
		return Query{}, ErrNoSelector
	}
	if q.Limit <= 0 {
		q.Limit = defaultReadLimit
	}
	if q.Limit > MaxReadLimit {
		q.Limit = MaxReadLimit
	}
	return q, nil
}

// Read answers a query against this logbook — LDV's extensie lezen.
//
// Who may do this is the open governance question (Q-08): a logbook holds a
// record of every processing about a person, so unrestricted read access
// recreates the very concentration the pseudonymisation avoids. The demo
// protects it with a separate bearer token and says no more than that.
func (l *Logbook) Read(ctx context.Context, query Query) ([]Stored, error) {
	normalized, err := query.normalize()
	if err != nil {
		return nil, err
	}
	records, err := l.repository.Query(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("read records: %w", err)
	}
	return records, nil
}
