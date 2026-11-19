// Package usage is the core of the data usage index: a list of pointers
// that say "under this legal basis, this logbook recorded a delivery under
// this trace". The index holds no personal data and nothing from the source;
// what was delivered stays in the source's own logbook (#687, where the index
// is called the trace index).
//
// Two use cases: a source registers a trace it delivered under (Register),
// and the consent register looks up the registrations of one consent
// (Lookup). The calling peer of a registration must be the peer its logbook
// ID names, so no organisation can register a delivery in another's name.
package usage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// GrondslagConsent is the only legal basis the index knows today: a consent
// from the consent register, referred to by its consent_id.
const GrondslagConsent = "consent"

// Registration is one pointer. It is unique on logbook ID, trace ID and legal
// basis, so a retry of the same registration finds the one already stored.
//
// No data subject fields: #687 lists them, but nothing can fill them until
// BSNk pseudonyms reach the index, and an empty field invites a wrong
// assumption about what the index holds.
type Registration struct {
	// LogbookID names the logbook that holds the record: the source peer's
	// FSC Peer ID, a slash and the name of the logbook on that peer. The name
	// is needed because one peer can run several (BD and LVG share ...0200).
	LogbookID     string
	TraceID       string
	GrondslagType string
	GrondslagRef  string
	RegisteredAt  time.Time
}

// Store keeps registrations durably.
type Store interface {
	// Add stores the registration unless an equal one (same logbook, trace
	// and legal basis) exists. It returns what is stored, and whether this
	// call created it.
	Add(ctx context.Context, registration Registration) (Registration, bool, error)
	// ByGrondslag returns every registration under one legal basis.
	ByGrondslag(ctx context.Context, grondslagType, grondslagRef string) ([]Registration, error)
}

// ErrInvalid marks a request the index refuses for its content. The wrapped
// message says what was wrong and is safe to return to the caller.
var ErrInvalid = errors.New("invalid registration")

// ErrNotOverFSC marks a registration that did not arrive through an Inway.
var ErrNotOverFSC = errors.New("a registration is only accepted over FSC")

// ErrForeignLogbook marks a registration for a logbook of another peer.
var ErrForeignLogbook = errors.New("the logbook ID names another peer than the caller")

var (
	peerIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{20}$`)
	// The logbook name is a single lowercase path segment: it ends up in the
	// register's configuration and in what the citizen's page names as the
	// source, so it is kept boring.
	logbookNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// W3C trace-id: 32 lowercase hex, not all zeros. The same form the Logius
	// read extension uses (OpenAPI 1.1.0).
	traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// A consent_id is "c-" plus a UUID; this admits that and other opaque
	// identifiers without admitting whitespace or separators.
	referencePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// Index is the use-case entry point.
type Index struct {
	store Store
	now   func() time.Time
}

// New returns an index over the store. now may be nil.
func New(store Store, now func() time.Time) *Index {
	if now == nil {
		now = time.Now
	}
	return &Index{store: store, now: now}
}

// Request is a registration as a source sends it.
type Request struct {
	LogbookID     string
	TraceID       string
	GrondslagType string
	GrondslagRef  string
}

// Register stores the registration of one delivery. callingPeer is the FSC
// Peer ID the Inway authenticated; empty means the request did not come
// through FSC.
//
// The index does not check the consent: whether it was valid was the PDP's
// decision at the source's Inway, before the delivery happened (decided 16-18
// Nov). It does check that the caller registers for its own logbook.
func (i *Index) Register(ctx context.Context, callingPeer string, request Request) (Registration, bool, error) {
	if callingPeer == "" {
		return Registration{}, false, ErrNotOverFSC
	}
	peer, err := ValidateLogbookID(request.LogbookID)
	if err != nil {
		return Registration{}, false, err
	}
	if !ValidTraceID(request.TraceID) {
		return Registration{}, false, fmt.Errorf("%w: trace_id must be 32 lowercase hex characters, not all zeros", ErrInvalid)
	}
	if err := validateGrondslag(request.GrondslagType, request.GrondslagRef); err != nil {
		return Registration{}, false, err
	}
	if peer != callingPeer {
		return Registration{}, false, ErrForeignLogbook
	}
	return i.store.Add(ctx, Registration{
		LogbookID:     request.LogbookID,
		TraceID:       request.TraceID,
		GrondslagType: request.GrondslagType,
		GrondslagRef:  request.GrondslagRef,
		RegisteredAt:  i.now().UTC(),
	})
}

// Lookup returns the registrations under one legal basis, newest first.
func (i *Index) Lookup(ctx context.Context, grondslagType, grondslagRef string) ([]Registration, error) {
	if err := validateGrondslag(grondslagType, grondslagRef); err != nil {
		return nil, err
	}
	registrations, err := i.store.ByGrondslag(ctx, grondslagType, grondslagRef)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(registrations, func(a, b int) bool {
		return registrations[a].RegisteredAt.After(registrations[b].RegisteredAt)
	})
	return registrations, nil
}

// ValidateLogbookID checks the form <Peer ID>/<logbook name> and returns the
// Peer ID.
func ValidateLogbookID(logbookID string) (string, error) {
	peer, name, ok := strings.Cut(logbookID, "/")
	if !ok || !peerIDPattern.MatchString(peer) || !logbookNamePattern.MatchString(name) {
		return "", fmt.Errorf("%w: logbook_id must be <20-character Peer ID>/<logbook name>", ErrInvalid)
	}
	return peer, nil
}

// ValidTraceID reports whether id is a W3C trace-id in its lowercase hex form.
func ValidTraceID(id string) bool {
	return traceIDPattern.MatchString(id) && id != strings.Repeat("0", 32)
}

func validateGrondslag(grondslagType, grondslagRef string) error {
	if grondslagType != GrondslagConsent {
		return fmt.Errorf("%w: grondslag_type must be %q", ErrInvalid, GrondslagConsent)
	}
	if !referencePattern.MatchString(grondslagRef) {
		return fmt.Errorf("%w: grondslag_ref must be 1 to 128 characters of letters, digits and . _ : -", ErrInvalid)
	}
	return nil
}
