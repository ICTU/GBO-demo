package consent

import (
	"context"
	"fmt"
	"time"
)

// Portal is the citizen-side orchestration. It owns the BSN boundary and
// every domain rule: which consent is effectively active, who may revoke
// what, and what the steps of a flow are. It knows nothing about HTTP.
type Portal struct {
	Pseudonyms Pseudonymizer
	Consents   Store
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

// Emit delivers an event to the portal's own watchers plus whatever
// request-scoped observer the caller put on the context (the per-request call
// log). Routing both through here keeps one source of truth: a core method
// called without an HTTP request still narrates to the panel.
func (p *Portal) Emit(ctx context.Context, e Event) {
	FanOut{p.Watch, ObserverFrom(ctx)}.Observe(ctx, e)
}

// stepEmitter returns a helper that emits panel steps for one flow.
func (p *Portal) stepEmitter(ctx context.Context, flow string) func(step, component string, data any) {
	return func(step, component string, data any) {
		p.Emit(ctx, Event{Flow: flow, Step: step, Component: component, Data: data})
	}
}

// GiveInput is one citizen's consent grant, before it has a subject.
type GiveInput struct {
	DienstverlenerOIN string
	Scopes            []string
	ScopeEntries      []ScopeEntry
	ValiditySeconds   int
	Trigger           string // "dev-portal" when the dev-portal drove this; narrative only
}

// Granted is the result of a successful grant.
type Granted struct {
	ConsentID string
	Pseudonym string
	PI        PI
}

// GiveConsent pseudonymises the citizen and registers the consent under the
// resulting PI.
//
// The ordering here is the privacy invariant: pseudonymisation happens first,
// and nothing below that line holds a BSN it could hand onwards — Draft has
// no field that would accept one.
func (p *Portal) GiveConsent(ctx context.Context, citizen BSN, in GiveInput) (Granted, error) {
	emit := p.stepEmitter(ctx, "give_consent")

	// Light up the portal component first: the citizen-side flow arrives
	// here before it fans out to BSNk and the consent register.
	emit("portal_received", "toestemmingsportaal", map[string]any{"oin": in.DienstverlenerOIN})

	// recipient_oin is the dienstverlener that will receive the pseudonym;
	// PI is the (recipient-independent) subject for the consent register.
	emit("pseudonymizing", "bsnk-mock", map[string]any{"oin": in.DienstverlenerOIN})
	ps, err := p.Pseudonyms.Pseudonymize(ctx, citizen, in.DienstverlenerOIN)
	if err != nil {
		return Granted{}, fmt.Errorf("pseudonymize: %w", err)
	}
	emit("pseudonym_generated", "bsnk-mock", map[string]any{"pseudonym": ps.Pseudonym, "pi": ps.PI})

	emit("consent_granting", "consent-register", map[string]any{"pi": ps.PI, "oin": in.DienstverlenerOIN})
	rec, err := p.Consents.Create(ctx, Draft{
		Subject:           ps.PI,
		DienstverlenerOIN: in.DienstverlenerOIN,
		Scopes:            in.Scopes,
		ScopeEntries:      in.ScopeEntries,
		UseCase:           UseCase,
		ValiditySeconds:   in.ValiditySeconds,
	})
	if err != nil {
		return Granted{}, fmt.Errorf("create consent: %w", err)
	}
	emit("consent_granted", "consent-register", map[string]any{"consent_id": rec.ID})

	return Granted{ConsentID: rec.ID, Pseudonym: ps.Pseudonym, PI: ps.PI}, nil
}

// ListConsents returns the calling citizen's consents, annotated with the
// effective status the UI renders. Filtering is by PI, which is what enforces
// per-citizen isolation.
func (p *Portal) ListConsents(ctx context.Context, citizen BSN) ([]Record, error) {
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
		return err // already wraps ErrNotFound on 404
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
