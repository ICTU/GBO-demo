package ldv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	ldvclient "gbo-demo/ldv-client"
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
	// Get returns the record already stored under this identity, if any.
	Get(ctx context.Context, traceID, spanID string) (Stored, bool, error)
	// Query answers a read. The query has already been validated and capped.
	Query(ctx context.Context, query Query) ([]Stored, error)
}

// ErrDuplicateRecord is returned when a record with the same (trace_id,
// span_id) is already stored. A producer that retries after a timeout will
// hit this; the logbook treats an identical replay as success rather than as a
// second Dataverwerking, because span ids identify the operation, not the
// attempt.
var ErrDuplicateRecord = errors.New("record already stored")

// ErrConflictingRecord is a different record under an identity that is
// already taken. Confirming it would tell the producer its Dataverwerking is
// logged while the logbook holds something else — the one lie a logbook must
// not tell. The producer has a bug, and gets told so.
var ErrConflictingRecord = errors.New("a different record is already stored under this identity")

// NormalizeTraceID turns a UUID-shaped correlator into an OTel trace id, or
// returns it unchanged when it already is one. The read extension types
// traceId as a uuid while records store the hyphen-free form, so a caller may
// legitimately send either.
func NormalizeTraceID(value string) string {
	if normalized := ldvclient.NormalizeTraceID(value); normalized != "" {
		return normalized
	}
	return strings.TrimSpace(value)
}

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
		// Idempotent only if it really is the same Dataverwerking. A replay
		// after a timeout is; a different record that happens to collide is
		// not, and confirming it would leave the producer believing something
		// the logbook does not hold.
		existing, found, getErr := l.repository.Get(ctx, record.TraceID, record.SpanID)
		if getErr != nil {
			return Confirmation{}, fmt.Errorf("compare with stored record: %w", getErr)
		}
		if !found || !existing.SameProcessingAs(record) {
			return Confirmation{}, ErrConflictingRecord
		}
		return Confirmation{TraceID: record.TraceID, SpanID: record.SpanID, ReceivedAt: existing.ReceivedAt, Duplicate: true}, nil
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
	// StartTime and EndTime narrow a result to a window. They are not
	// selectors: a read still needs one of the three axes, because a window
	// alone would return every Betrokkene in it.
	StartTime *time.Time
	EndTime   *time.Time
	Limit     int
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

// Page is a capped answer plus whether the cap cut it short.
type Page struct {
	Records []Stored
	// Truncated says there was more. Derived from asking storage for one
	// record beyond the limit rather than from comparing the count with it:
	// a result that happens to be exactly the limit is not truncated, and a
	// caller told otherwise would go looking for records that do not exist.
	Truncated bool
}

// Read answers a query against this logbook — LDV's extensie lezen.
//
// Who may do this is the open governance question (Q-08): a logbook holds a
// record of every processing about a person, so unrestricted read access
// recreates the very concentration the pseudonymisation avoids. The demo
// protects it with a separate bearer token and says no more than that.
func (l *Logbook) Read(ctx context.Context, query Query) (Page, error) {
	normalized, err := query.normalize()
	if err != nil {
		return Page{}, err
	}
	limit := normalized.Limit
	probe := normalized
	probe.Limit = limit + 1
	records, err := l.repository.Query(ctx, probe)
	if err != nil {
		return Page{}, fmt.Errorf("read records: %w", err)
	}
	if len(records) > limit {
		return Page{Records: records[:limit], Truncated: true}, nil
	}
	return Page{Records: records}, nil
}
