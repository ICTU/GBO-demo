package consent

import (
	"context"
	"time"
)

// The driven ports, declared here at their consumer. Each is implemented by
// exactly one adapter package (bsnk, register) and by in-memory fakes in the
// tests. "Accept interfaces, return structs": the adapters are plain structs.

// Identities is BSNk seen from the core when a citizen gives consent: the BSN
// goes in, and what comes out is an encrypted identity per party that only
// that party can read.
//
// BSNk does this in two steps. It activates the BSN into polymorphic values
// that only the portal can use, and transforms those into a value per party.
// The polymorphic values live for the duration of this one call and leave the
// portal nowhere, so the core does not see them.
//
// A party that may not receive the BSN makes the whole call fail: a consent
// whose source cannot read the subject would be a consent nobody can use.
type Identities interface {
	EncryptFor(ctx context.Context, bsn BSN, parties []Party) ([]EncryptedSubject, error)
}

// Pseudonymizer derives the portal's own reference to a citizen: a pseudonym
// scoped to the recipient it is asked for. One RPC, no state.
type Pseudonymizer interface {
	Pseudonymize(ctx context.Context, bsn BSN, recipientOIN string) (string, error)
}

// Store is the consent register seen from the core. Citizen listing is keyed
// by a portal-scoped SubjectRef; Get and Revoke address a consent ID. Those
// methods must return an error wrapping ErrNotFound when the record does not
// exist, so the core can tell "missing" from "upstream broke".
type Store interface {
	Create(ctx context.Context, d Draft) (Record, error)
	ListBySubject(ctx context.Context, subject SubjectRef) ([]Record, error)
	Get(ctx context.Context, consentID string) (Record, error)
	Revoke(ctx context.Context, consentID string) error
}

// Processing is one Dataverwerking of this Verantwoordelijke, as the core
// knows it: what was done, to whose data, when, and whether it worked.
//
// It lives in the core rather than in the adapter because logging a
// processing is a rule about the processing, not about a transport. The
// adapter turns this into the OTel-shaped record the LDV standard defines;
// the core does not know that shape and does not need to.
type Processing struct {
	// Activity is the reference into the Verantwoordelijke's register of
	// verwerkingsactiviteiten, in `<id>@<version>` form.
	Activity string
	// Name is the short name of the processing, for a human reading the log.
	Name string
	// Subject is the Betrokkene, named by the portal-scoped reference. Never
	// a BSN and never a value made for another party: the reference the
	// register lists a citizen by is the only identifier this side is allowed
	// to write down.
	Subject SubjectRef
	Start   time.Time
	End     time.Time
	Failed  bool
	// NextLogbook is where this processing continues when it called another
	// party: the read API of that party's logbook, or a contact page when it
	// has none. Empty when nothing else was called.
	NextLogbook string
	// Attributes is whatever local detail helps a reader; the adapter
	// prefixes nothing, so callers pass fully-qualified `gbo.` keys.
	Attributes map[string]any
}

// Logbook is the Verantwoordelijke's Logboek Dataverwerkingen seen from the
// core. One method, and it must have been confirmed by the logbook before it
// returns nil.
//
// A nil Logbook means this deployment is not part of an LDV chain and the
// portal writes no records. When one is set, Record's error is propagated:
// a processing that cannot be logged does not complete.
type Logbook interface {
	Record(ctx context.Context, p Processing) error
}
