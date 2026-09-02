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
	Logbook    Logbook          // Logboek Dataverwerkingen; nil means not in an LDV chain
	OwnOIN     string           // recipient_oin used for the portal-scoped subject reference
	Now        func() time.Time // nil means time.Now; injected by tests
}

// The verwerkingsactiviteiten of this portal, as named in GBO's register.
const pseudonymisationActivity = "gbo-bsn-pseudonimisering@v1"

// record files one Dataverwerking. Nil-safe, so callers need no branch: a
// deployment without a logbook is simply not in an LDV chain.
//
// The error is returned rather than swallowed. Everything this portal logs
// happens before the processing it belongs to has any outward effect, so a
// caller that propagates the error genuinely prevents an unlogged processing
// rather than merely reporting one.
func (p *Portal) record(ctx context.Context, processing Processing) error {
	if p.Logbook == nil {
		return nil
	}
	return p.Logbook.Record(ctx, processing)
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
	ConsentID    string
	ConsentToken string
}

// GiveConsent derives the authorization PI and a separate portal-scoped
// subject reference, then asks the consent register to persist the latter and sign the former.
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
	// PI becomes signed authorization material for that dienstverlener.
	pseudonymisationStart := p.now().UTC()
	emit("pseudonymizing", "bsnk-mock", map[string]any{"oin": in.DienstverlenerOIN})
	ps, err := p.Pseudonyms.Pseudonymize(ctx, citizen, in.DienstverlenerOIN)
	if err != nil {
		return Granted{}, fmt.Errorf("pseudonymize: %w", err)
	}
	emit("pseudonym_generated", "bsnk-mock", map[string]any{"pseudonym": ps.Pseudonym})

	// The consent register needs a subject key for citizen listing, but must not persist PI.
	// A second BSNk derivation scoped to the portal provides that key.
	portalSubject, err := p.Pseudonyms.Pseudonymize(ctx, citizen, p.OwnOIN)
	if err != nil {
		return Granted{}, fmt.Errorf("derive portal subject reference: %w", err)
	}

	// Turning a BSN into pseudonyms is itself a Dataverwerking — the one that
	// makes every processing after it BSN-free — so it is logged here, before
	// the consent is created. A record that the logbook does not confirm
	// therefore leaves no consent behind.
	//
	// It is logged under the reference it just produced, which is the only
	// identifier this portal may write down: not the BSN it started from, and
	// not the PI it derived for the dienstverlener.
	if err := p.record(ctx, Processing{
		Activity: pseudonymisationActivity,
		Name:     "dataverwerking.bsn-pseudonimisering",
		Subject:  SubjectRef(portalSubject.Pseudonym),
		Start:    pseudonymisationStart,
		End:      p.now().UTC(),
		Attributes: map[string]any{
			"gbo.pseudonimisering.aanleiding": "toestemming-verlenen",
			"gbo.consent.dienstverlener":      in.DienstverlenerOIN,
		},
	}); err != nil {
		return Granted{}, fmt.Errorf("log pseudonymisation: %w", err)
	}

	emit("consent_granting", "consent-register", map[string]any{"oin": in.DienstverlenerOIN})
	rec, err := p.Consents.Create(ctx, Draft{
		PI:                ps.PI,
		SubjectRef:        SubjectRef(portalSubject.Pseudonym),
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

	return Granted{ConsentID: rec.ID, ConsentToken: rec.Token}, nil
}

// ListConsents returns the calling citizen's consents, annotated with the
// effective status the UI renders. Filtering is by a portal-scoped subject
// reference, which enforces per-citizen isolation without persisting PI.
func (p *Portal) ListConsents(ctx context.Context, citizen BSN) ([]Record, error) {
	subjectRef, err := p.subjectRefFor(ctx, citizen, "toestemmingen-inzien")
	if err != nil {
		return nil, err
	}
	recs, err := p.Consents.ListBySubject(ctx, subjectRef)
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

	subjectRef, err := p.subjectRefFor(ctx, citizen, "toestemming-intrekken")
	if err != nil {
		return err
	}
	rec, err := p.Consents.Get(ctx, consentID)
	if err != nil {
		return err // already wraps ErrNotFound on 404
	}
	// Ownership is an authorization rule about the citizen, so it lives here.
	// The register adapter cannot see who is calling, and must not.
	if rec.SubjectRef != subjectRef {
		return ErrNotOwned
	}

	emit("consent_revoking", "consent-register", map[string]any{"consent_id": consentID})
	if err := p.Consents.Revoke(ctx, consentID); err != nil {
		return fmt.Errorf("revoke: %w", err)
	}
	emit("consent_revoked", "consent-register", map[string]any{"consent_id": consentID})
	return nil
}

// subjectRefFor derives a portal-specific pseudonym for citizen-facing lookup.
// This is deliberately not the recipient-independent PI.
//
// The derivation is a Dataverwerking wherever it happens — listing and
// revoking both start with it — so it is logged here rather than at each call
// site, where one of them would eventually be forgotten. `aanleiding` says
// which flow asked.
func (p *Portal) subjectRefFor(ctx context.Context, citizen BSN, aanleiding string) (SubjectRef, error) {
	start := p.now().UTC()
	ps, err := p.Pseudonyms.Pseudonymize(ctx, citizen, p.OwnOIN)
	if err != nil {
		return "", fmt.Errorf("pseudonymize: %w", err)
	}
	if err := p.record(ctx, Processing{
		Activity:   pseudonymisationActivity,
		Name:       "dataverwerking.bsn-pseudonimisering",
		Subject:    SubjectRef(ps.Pseudonym),
		Start:      start,
		End:        p.now().UTC(),
		Attributes: map[string]any{"gbo.pseudonimisering.aanleiding": aanleiding},
	}); err != nil {
		return "", fmt.Errorf("log pseudonymisation: %w", err)
	}
	return SubjectRef(ps.Pseudonym), nil
}
