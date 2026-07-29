package main

// This file is the domain core. It depends only on the port interfaces
// declared below — never on net/http, URLs, or JSON wire shapes. Keep it
// that way: if you find yourself importing net/http here, the logic you are
// adding belongs in an adapter instead.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BSN is a plain citizen identifier. It may cross exactly one port:
// Pseudonymizer. No ConsentStore method accepts a BSN, so handing one to the
// consent register is a compile error rather than something a reviewer has to
// catch. That is the whole privacy promise of this service, in the type
// system.
type BSN string

// PI is the recipient-independent pseudonymous identity BSNk derives from a
// BSN. It is the only subject the consent register ever sees.
type PI string

// Status is the consent status as the citizen experiences it, derived from
// the register's own status plus the clock.
type Status string

const (
	StatusActive  Status = "active"
	StatusExpired Status = "expired"
	StatusRevoked Status = "revoked"
)

// useCase labels every consent this portal creates. The demo has one.
const useCase = "hypotheek"

var (
	// ErrConsentNotFound is returned when the register has no such record.
	ErrConsentNotFound = errors.New("consent not found")
	// ErrNotOwned is returned when the caller's PI does not match the
	// record's subject. Without this check any token holder could revoke
	// any consent_id they guess.
	ErrNotOwned = errors.New("consent does not belong to authenticated citizen")
)

// ── Ports ─────────────────────────────────────────────────────────────────

// Pseudonyms is what BSNk returns: a recipient-scoped pseudonym plus the
// recipient-independent PI used as the register's subject key.
type Pseudonyms struct {
	Pseudonym string
	PI        PI
}

// Pseudonymizer is BSNk seen from the core: one RPC, no state. This is the
// only port allowed to see a BSN.
type Pseudonymizer interface {
	Pseudonymize(ctx context.Context, bsn BSN, recipientOIN string) (Pseudonyms, error)
}

// ScopeEntry is the per-bronhouder field selection the citizen consented to.
type ScopeEntry struct {
	Bronhouder      string   `json:"bronhouder"`
	ScopeID         string   `json:"scope_id"`
	ConsentedFields []string `json:"consented_fields"`
}

// NewConsent is a consent about to be registered. Its subject is a PI: there
// is deliberately no BSN field.
type NewConsent struct {
	Subject           PI
	DienstverlenerOIN string
	Scopes            []string
	ScopeEntries      []ScopeEntry
	UseCase           string
	ValiditySeconds   int
}

// ConsentRecord is a register record as the core understands it: the few
// fields the core reasons about, plus the register's own payload passed
// through untouched so the UI keeps working when the register grows a field.
//
// Adapters fill ID/Subject/Status/ValidUntil/Raw. Only the core sets
// Effective — computing it is a judgement, and judgements are core work.
type ConsentRecord struct {
	ID         string
	Subject    PI
	Status     string    // as reported by the register, e.g. "ACTIVE"/"REVOKED"
	ValidUntil time.Time // zero value means no expiry
	Raw        map[string]any
	Effective  Status
}

// EffectiveStatus is the rule the citizen-facing UI depends on: a consent is
// revoked if the register says so, expired if its validity has passed, and
// active otherwise. Pure, so it is table-testable without any I/O.
func (c ConsentRecord) EffectiveStatus(now time.Time) Status {
	if strings.EqualFold(c.Status, "REVOKED") {
		return StatusRevoked
	}
	if !c.ValidUntil.IsZero() && c.ValidUntil.Before(now) {
		return StatusExpired
	}
	return StatusActive
}

// ConsentStore is the consent register seen from the core: CRUD keyed by PI.
// Get and Revoke must return an error wrapping ErrConsentNotFound when the
// record does not exist, so the core can tell "missing" from "upstream broke".
type ConsentStore interface {
	Create(ctx context.Context, c NewConsent) (ConsentRecord, error)
	ListBySubject(ctx context.Context, subject PI) ([]ConsentRecord, error)
	Get(ctx context.Context, consentID string) (ConsentRecord, error)
	Revoke(ctx context.Context, consentID string) error
}

// ── Core ──────────────────────────────────────────────────────────────────

// Portal is the citizen-side orchestration. It owns the BSN boundary and
// every domain rule: which consent is effectively active, who may revoke
// what, and what the steps of a flow are. It knows nothing about HTTP.
type Portal struct {
	Pseudonyms Pseudonymizer
	Consents   ConsentStore
	Watch      Observer         // process-lifetime watchers; nil is fine
	OwnOIN     string           // recipient_oin used when the portal needs a PI for its own sake
	Now        func() time.Time // nil means time.Now; injected by tests
}

func (p *Portal) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// observe delivers an event to the portal's own watchers plus whatever
// request-scoped observer the caller put on the context (the per-request call
// log). Routing both through here keeps one source of truth: a core method
// called without an HTTP request still narrates to the SSE panel.
func (p *Portal) observe(ctx context.Context, e Event) {
	FanOut{p.Watch, observerFrom(ctx)}.Observe(ctx, e)
}

// stepEmitter returns a helper that emits panel steps for one flow.
func (p *Portal) stepEmitter(ctx context.Context, flow string) func(step, component string, data any) {
	return func(step, component string, data any) {
		p.observe(ctx, Event{Flow: flow, Step: step, Component: component, Data: data})
	}
}

// GiveConsentInput is one citizen's consent grant, before it has a subject.
type GiveConsentInput struct {
	DienstverlenerOIN string
	Scopes            []string
	ScopeEntries      []ScopeEntry
	ValiditySeconds   int
	Trigger           string // "dev-portal" when the dev-portal drove this; narrative only
}

// ConsentGranted is the result of a successful grant.
type ConsentGranted struct {
	ConsentID string
	Pseudonym string
	PI        PI
}

// GiveConsent pseudonymises the citizen and registers the consent under the
// resulting PI.
//
// The ordering here is the privacy invariant: pseudonymisation happens first,
// and nothing below that line holds a BSN it could hand onwards — NewConsent
// has no field that would accept one.
func (p *Portal) GiveConsent(ctx context.Context, citizen BSN, in GiveConsentInput) (ConsentGranted, error) {
	emit := p.stepEmitter(ctx, "give_consent")

	// Light up the portal component first: the citizen-side flow arrives
	// here before it fans out to BSNk and the consent register.
	emit("portal_received", "toestemmingsportaal", map[string]any{"oin": in.DienstverlenerOIN})

	// recipient_oin is the dienstverlener that will receive the pseudonym;
	// PI is the (recipient-independent) subject for the consent register.
	emit("pseudonymizing", "bsnk-mock", map[string]any{"oin": in.DienstverlenerOIN})
	ps, err := p.Pseudonyms.Pseudonymize(ctx, citizen, in.DienstverlenerOIN)
	if err != nil {
		return ConsentGranted{}, fmt.Errorf("pseudonymize: %w", err)
	}
	emit("pseudonym_generated", "bsnk-mock", map[string]any{"pseudonym": ps.Pseudonym, "pi": ps.PI})

	emit("consent_granting", "consent-register", map[string]any{"pi": ps.PI, "oin": in.DienstverlenerOIN})
	rec, err := p.Consents.Create(ctx, NewConsent{
		Subject:           ps.PI,
		DienstverlenerOIN: in.DienstverlenerOIN,
		Scopes:            in.Scopes,
		ScopeEntries:      in.ScopeEntries,
		UseCase:           useCase,
		ValiditySeconds:   in.ValiditySeconds,
	})
	if err != nil {
		return ConsentGranted{}, fmt.Errorf("create consent: %w", err)
	}
	emit("consent_granted", "consent-register", map[string]any{"consent_id": rec.ID})

	return ConsentGranted{ConsentID: rec.ID, Pseudonym: ps.Pseudonym, PI: ps.PI}, nil
}

// ListConsents returns the calling citizen's consents, annotated with the
// effective status the UI renders. Filtering is by PI, which is what enforces
// per-citizen isolation.
func (p *Portal) ListConsents(ctx context.Context, citizen BSN) ([]ConsentRecord, error) {
	pi, err := p.piFor(ctx, citizen)
	if err != nil {
		return nil, err
	}
	recs, err := p.Consents.ListBySubject(ctx, pi)
	if err != nil {
		return nil, fmt.Errorf("list consents: %w", err)
	}
	now := p.now()
	for i := range recs {
		recs[i].Effective = recs[i].EffectiveStatus(now)
	}
	return recs, nil
}

// RevokeConsent revokes a consent after verifying it belongs to the caller.
func (p *Portal) RevokeConsent(ctx context.Context, citizen BSN, consentID string) error {
	emit := p.stepEmitter(ctx, "revoke_consent")
	emit("portal_received", "toestemmingsportaal", map[string]any{"consent_id": consentID})

	pi, err := p.piFor(ctx, citizen)
	if err != nil {
		return err
	}
	rec, err := p.Consents.Get(ctx, consentID)
	if err != nil {
		return err // already wraps ErrConsentNotFound on 404
	}
	// Ownership is an authorization rule about the citizen, so it lives here.
	// The register adapter cannot see who is calling, and must not.
	if rec.Subject != pi {
		return ErrNotOwned
	}

	emit("consent_revoking", "consent-register", map[string]any{"consent_id": consentID})
	if err := p.Consents.Revoke(ctx, consentID); err != nil {
		return fmt.Errorf("revoke: %w", err)
	}
	emit("consent_revoked", "consent-register", map[string]any{"consent_id": consentID})
	return nil
}

// piFor derives the caller's PI using the portal's own OIN as recipient. The
// PI BSNk returns is deterministic per BSN regardless of recipient_oin, so
// any value works; the portal's own OIN is the clearest narrative.
func (p *Portal) piFor(ctx context.Context, citizen BSN) (PI, error) {
	ps, err := p.Pseudonyms.Pseudonymize(ctx, citizen, p.OwnOIN)
	if err != nil {
		return "", fmt.Errorf("pseudonymize: %w", err)
	}
	return ps.PI, nil
}
