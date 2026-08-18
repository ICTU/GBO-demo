package consent

import "context"

// The driven ports, declared here at their consumer. Each is implemented by
// exactly one adapter package (bsnk, register) and by in-memory fakes in the
// tests. "Accept interfaces, return structs": the adapters are plain structs.

// Pseudonyms is what BSNk returns: a recipient-scoped pseudonym plus the
// recipient-independent PI used only inside the signed authorization context.
type Pseudonyms struct {
	Pseudonym string
	PI        PI
}

// Pseudonymizer is BSNk seen from the core: one RPC, no state. This is the
// only port allowed to see a BSN.
type Pseudonymizer interface {
	Pseudonymize(ctx context.Context, bsn BSN, recipientOIN string) (Pseudonyms, error)
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
