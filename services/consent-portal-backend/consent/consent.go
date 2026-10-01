// Package consent is the domain core of the citizen consent portal. It owns
// the BSN boundary and every consent rule; it knows nothing about HTTP.
//
// The package imports no transport library, and that is enforced by the
// compiler rather than by convention: nothing here may import net/http. If a
// change wants to reach for a URL or a JSON tag, it belongs in an adapter
// package (bsnk, register, devportal, portalhttp) instead.
package consent

import (
	"errors"
	"strings"
	"time"
)

// BSN is a plain citizen identifier. It may cross the two ports that talk to
// BSNk, Identities and Pseudonymizer, and no other. No Store method accepts a
// BSN, so handing one to the consent register is a compile error rather than
// something a reviewer has to catch. That is the whole privacy promise of
// this service, in the type system.
type BSN string

// SubjectRef is a pseudonym scoped to the consent portal. The consent register
// may persist it for citizen listing and ownership checks; it means nothing to
// a service provider or source holder.
type SubjectRef string

// Party is one recipient of the citizen's identity under a consent: a source
// that answers the service provider's question. BSNk makes a value for one
// party, identified by its OIN, and for one version of that party's keys.
type Party struct {
	OIN           string
	KeySetVersion int
}

// EncryptedSubject is the citizen as one party may read them. Only that party
// can decrypt the value, and the portal cannot: it is what goes into the
// consent token in place of any identifier.
type EncryptedSubject struct {
	Party Party
	// IdentifierType says what the value decrypts to, in BSNk's own words:
	// "Identity" is the BSN.
	IdentifierType string
	Value          string
}

// Status is the consent status as the citizen experiences it, derived from
// the register's own status plus the clock.
type Status string

const (
	StatusActive  Status = "active"
	StatusExpired Status = "expired"
	StatusRevoked Status = "revoked"
)

// DefaultUseCase labels a consent whose dienstverlener named no purpose. The
// hypotheek flow predates the portal passing the purpose on, and the
// dev-portal's scenarios still send none.
const DefaultUseCase = "hypotheek"

var (
	// ErrNotFound is returned when the register has no such record.
	ErrNotFound = errors.New("consent not found")
	// ErrNotOwned is returned when the caller's portal-scoped subject reference
	// does not match the record. Without this check any authenticated citizen
	// could revoke any consent_id they guess.
	ErrNotOwned = errors.New("consent does not belong to authenticated citizen")
)

// ScopeEntry is the per-bronhouder field selection the citizen consented to.
type ScopeEntry struct {
	Bronhouder      string   `json:"bronhouder"`
	ScopeID         string   `json:"scope_id"`
	ConsentedFields []string `json:"consented_fields"`
}

// Draft is a consent about to be registered. Subjects is transient token
// material; SubjectRef is the only persisted subject. There is deliberately no
// field that would accept a BSN.
type Draft struct {
	Subjects          []EncryptedSubject
	SubjectRef        SubjectRef
	DienstverlenerOIN string
	Scopes            []string
	ScopeEntries      []ScopeEntry
	UseCase           string
	ValiditySeconds   int
}

// Record is a register record as the core understands it: the few fields the
// core reasons about, plus the register's own payload passed through
// untouched so the UI keeps working when the register grows a field.
//
// Adapters fill ID/SubjectRef/Token/Status/ValidUntil/Raw. Only the core sets
// Effective — computing it is a judgement, and judgements are core work.
type Record struct {
	ID         string
	SubjectRef SubjectRef
	Token      string
	Status     string    // as reported by the register, e.g. "ACTIVE"/"REVOKED"
	ValidUntil time.Time // zero value means no expiry
	Raw        map[string]any
	Effective  Status
}

// EffectiveStatus is the rule the citizen-facing UI depends on: a consent is
// revoked if the register says so, expired if its validity has passed, and
// active otherwise. Pure, so it is table-testable without any I/O.
func (r Record) EffectiveStatus(now time.Time) Status {
	if strings.EqualFold(r.Status, "REVOKED") {
		return StatusRevoked
	}
	if !r.ValidUntil.IsZero() && r.ValidUntil.Before(now) {
		return StatusExpired
	}
	return StatusActive
}
