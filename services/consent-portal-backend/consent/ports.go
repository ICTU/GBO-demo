package consent

import (
	"context"
	"time"
)

// The driven ports, declared here at their consumer. Each is implemented by
// exactly one adapter package (bsnk, register) and by in-memory fakes in the
// tests. "Accept interfaces, return structs": the adapters are plain structs.

// Identities is BSNk seen from the core, in BSNk's own steps.
//
// Activate turns a BSN into the polymorphic values BSNk makes for the portal.
// It is the only port a BSN crosses, and the portal calls it once per
// citizen.
//
// AuthorisedForBSN reads BSNk's BSN authorisation list: the OINs of the
// parties that may receive the BSN.
//
// Transform has BSNk make a value of one form for each party, from the
// portal's polymorphic values, and returns them by OIN: a value for every
// party, or an error. BSNk allows it only while the citizen is present, so
// the portal calls it when a consent is given. A party BSNk refuses makes the
// whole call fail.
type Identities interface {
	Activate(ctx context.Context, bsn BSN) (Polymorphic, error)
	AuthorisedForBSN(ctx context.Context) ([]string, error)
	Transform(ctx context.Context, values Polymorphic, form Form, parties []Party) (map[string]string, error)
}

// Store is the consent register seen from the core. Citizen listing is keyed
// by a portal-scoped SubjectRef; Get and Revoke address a consent ID. Those
// methods must return an error wrapping ErrNotFound when the record does not
// exist, so the core can tell "missing" from "upstream broke".
//
// The register also keeps the polymorphic values of a citizen's first
// activation, under the portal's reference to them. PolymorphicFor returns
// an error wrapping ErrNotFound for a citizen who has none yet.
type Store interface {
	Create(ctx context.Context, d Draft) (Record, error)
	ListBySubject(ctx context.Context, subject SubjectRef) ([]Record, error)
	Get(ctx context.Context, consentID string) (Record, error)
	Revoke(ctx context.Context, consentID string) error
	PolymorphicFor(ctx context.Context, subject SubjectRef) (Polymorphic, error)
	KeepPolymorphic(ctx context.Context, subject SubjectRef, values Polymorphic) error
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
