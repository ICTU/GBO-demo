package consent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Portal is the citizen-side orchestration. It owns the BSN boundary and
// every domain rule: which consent is effectively active, who may revoke
// what, and what the steps of a flow are. It knows nothing about HTTP.
type Portal struct {
	Identities  Identities
	SubjectRefs SubjectRefs // derives the portal's own reference to a citizen
	Consents    Store
	Watch       Observer // process-lifetime watchers; nil is fine
	Logbook     Logbook  // Logboek Dataverwerkingen; nil means not in an LDV chain
	// Sources are the parties a consent's token carries encrypted values for.
	// BSNk makes those values only while the citizen is present, so every
	// source that will answer must be known here, when consent is given.
	Sources []Party
	// AuthorisationListMaxAge is how long the portal uses its copy of BSNk's
	// BSN authorisation list before reading it again. Zero reads it for every
	// consent.
	AuthorisationListMaxAge time.Duration
	Now                     func() time.Time // nil means time.Now; injected by tests
	// PseudonymsLogbook is where the pseudonymisation service's processings
	// can be looked up — its read API, or a contact page when it has none —
	// recorded as the pseudonymisation's next logbook. Empty means unknown.
	PseudonymsLogbook string

	authorisations authorisationList
}

// authorisationList is the portal's copy of BSNk's BSN authorisation list.
// The list is to be read periodically and not used once it is out of date;
// the copy is therefore read again after AuthorisationListMaxAge, and a copy
// that cannot be renewed is not used at all.
type authorisationList struct {
	mu      sync.Mutex
	listed  map[string]bool
	fetched time.Time
}

// The verwerkingsactiviteiten of this portal, as named in GBO's register.
const pseudonymisationActivity = "https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-bsn-pseudonimisering/v1"

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
	UseCase           string // the dienstverlener's stated purpose; DefaultUseCase when empty
	Trigger           string // "dev-portal" when the dev-portal drove this; narrative only
}

// Granted is the result of a successful grant.
type Granted struct {
	ConsentID    string
	ConsentToken string
}

// GiveConsent derives the portal's own reference to the citizen, has BSNk
// make encrypted values for each source, then asks the consent register to
// persist the reference and sign the values into the consent token. Every
// source gets a pseudonym; a source on BSNk's BSN authorisation list also gets
// an identity.
//
// BSNk activates a citizen once. At their first consent the portal activates
// and keeps the polymorphic values; at every consent after that it finds them
// under the reference and only has them transformed.
//
// The service provider gets no identifier of the citizen at all: the token
// carries values only the sources can read.
//
// The ordering here is the privacy invariant: everything that needs the BSN
// happens first, and nothing below that line holds a BSN it could hand
// onwards — Draft has no field that would accept one.
func (p *Portal) GiveConsent(ctx context.Context, citizen BSN, in GiveInput) (Granted, error) {
	emit := p.stepEmitter(ctx, "give_consent")

	// Light up the portal component first: the citizen-side flow arrives
	// here before it fans out to BSNk and the consent register.
	emit("portal_received", "toestemmingsportaal", map[string]any{"oin": in.DienstverlenerOIN})

	if len(p.Sources) == 0 {
		return Granted{}, errors.New("no sources configured: a consent no source can read is not given")
	}
	recipients := make([]string, len(p.Sources))
	for i, source := range p.Sources {
		recipients[i] = source.OIN
	}

	pseudonymisationStart := p.now().UTC()
	// The consent register needs a subject key for citizen listing, and the
	// values made below are readable by the sources alone.
	portalSubject, err := p.SubjectRefs.For(citizen)
	if err != nil {
		return Granted{}, fmt.Errorf("derive portal subject reference: %w", err)
	}

	emit("pseudonymizing", "bsnk-mock", map[string]any{"recipients": recipients})
	values, err := p.polymorphicFor(ctx, citizen, portalSubject)
	if err != nil {
		return Granted{}, err
	}
	subjects, err := p.encrypt(ctx, values)
	if err != nil {
		return Granted{}, fmt.Errorf("encrypt the subject for the sources: %w", err)
	}
	emit("pseudonym_generated", "bsnk-mock", map[string]any{"recipients": recipients})

	// Turning a BSN into pseudonyms is itself a Dataverwerking — the one that
	// makes every processing after it BSN-free — so it is logged here, before
	// the consent is created. A record that the logbook does not confirm
	// therefore leaves no consent behind.
	//
	// It is logged under the reference it just produced, which is the only
	// identifier this portal may write down: not the BSN it started from, and
	// not a value it had made for a source.
	if err := p.record(ctx, Processing{
		Activity: pseudonymisationActivity,
		Name:     "dataverwerking.bsn-pseudonimisering",
		Subject:  portalSubject,
		Start:    pseudonymisationStart,
		End:      p.now().UTC(),
		// BSNk did the transform; its side of it is logged there, not here.
		NextLogbook: p.PseudonymsLogbook,
		Attributes: map[string]any{
			"dpl.gbo.pseudonimiseringAanleiding": "toestemming-verlenen",
		},
	}); err != nil {
		return Granted{}, fmt.Errorf("log pseudonymisation: %w", err)
	}

	emit("consent_granting", "consent-register", map[string]any{"oin": in.DienstverlenerOIN})
	rec, err := p.Consents.Create(ctx, Draft{
		Subjects:          subjects,
		SubjectRef:        portalSubject,
		DienstverlenerOIN: in.DienstverlenerOIN,
		Scopes:            in.Scopes,
		ScopeEntries:      in.ScopeEntries,
		UseCase:           useCaseOrDefault(in.UseCase),
		ValiditySeconds:   in.ValiditySeconds,
	})
	if err != nil {
		return Granted{}, fmt.Errorf("create consent: %w", err)
	}
	emit("consent_granted", "consent-register", map[string]any{"consent_id": rec.ID})

	return Granted{ConsentID: rec.ID, ConsentToken: rec.Token}, nil
}

// encrypt has BSNk make what the token carries for each source: a pseudonym
// for every one, and an identity for the ones on the BSN authorisation list.
// A party occurs once in a transformation, so the two forms take a call each.
func (p *Portal) encrypt(ctx context.Context, values Polymorphic) ([]EncryptedSubject, error) {
	listed, err := p.authorisedForBSN(ctx)
	if err != nil {
		return nil, err
	}
	pseudonyms, err := p.Identities.Transform(ctx, values, FormPseudonym, p.Sources)
	if err != nil {
		return nil, err
	}
	var authorised []Party
	for _, source := range p.Sources {
		if listed[source.OIN] {
			authorised = append(authorised, source)
		}
	}
	identities := map[string]string{}
	if len(authorised) > 0 {
		if identities, err = p.Identities.Transform(ctx, values, FormIdentity, authorised); err != nil {
			return nil, err
		}
	}

	subjects := make([]EncryptedSubject, len(p.Sources))
	for i, source := range p.Sources {
		subjects[i] = EncryptedSubject{Party: source, Pseudonym: pseudonyms[source.OIN], Identity: identities[source.OIN]}
	}
	return subjects, nil
}

// authorisedForBSN returns the parties that may receive the BSN, from a copy
// of BSNk's list that is no older than AuthorisationListMaxAge.
func (p *Portal) authorisedForBSN(ctx context.Context) (map[string]bool, error) {
	list := &p.authorisations
	list.mu.Lock()
	defer list.mu.Unlock()

	now := p.now()
	if list.listed != nil && now.Sub(list.fetched) < p.AuthorisationListMaxAge {
		return list.listed, nil
	}
	oins, err := p.Identities.AuthorisedForBSN(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the BSN authorisation list: %w", err)
	}
	listed := make(map[string]bool, len(oins))
	for _, oin := range oins {
		listed[oin] = true
	}
	list.listed, list.fetched = listed, now
	return listed, nil
}

// ListConsents returns the calling citizen's consents, annotated with the
// effective status the UI renders. Filtering is by a portal-scoped subject
// reference, which enforces per-citizen isolation.
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

// polymorphicFor returns the polymorphic values the portal keeps for this
// citizen. A citizen without any is activated here — once, at their first
// consent — and what BSNk returned is kept for every consent after it.
func (p *Portal) polymorphicFor(ctx context.Context, citizen BSN, subject SubjectRef) (Polymorphic, error) {
	values, err := p.Consents.PolymorphicFor(ctx, subject)
	if err == nil {
		return values, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Polymorphic{}, fmt.Errorf("find the polymorphic values: %w", err)
	}
	values, err = p.Identities.Activate(ctx, citizen)
	if err != nil {
		return Polymorphic{}, fmt.Errorf("activate: %w", err)
	}
	if err := p.Consents.KeepPolymorphic(ctx, subject, values); err != nil {
		return Polymorphic{}, fmt.Errorf("keep the polymorphic values: %w", err)
	}
	return values, nil
}

// subjectRefFor derives the portal's own reference for citizen-facing lookup.
//
// The derivation is a Dataverwerking wherever it happens — listing and
// revoking both start with it — so it is logged here rather than at each call
// site, where one of them would eventually be forgotten. `aanleiding` says
// which flow asked. The portal derives the reference itself and calls nobody,
// so the record points to no other logbook.
func (p *Portal) subjectRefFor(ctx context.Context, citizen BSN, aanleiding string) (SubjectRef, error) {
	start := p.now().UTC()
	subject, err := p.SubjectRefs.For(citizen)
	if err != nil {
		return "", fmt.Errorf("derive portal subject reference: %w", err)
	}
	if err := p.record(ctx, Processing{
		Activity:   pseudonymisationActivity,
		Name:       "dataverwerking.bsn-pseudonimisering",
		Subject:    subject,
		Start:      start,
		End:        p.now().UTC(),
		Attributes: map[string]any{"dpl.gbo.pseudonimiseringAanleiding": aanleiding},
	}); err != nil {
		return "", fmt.Errorf("log pseudonymisation: %w", err)
	}
	return subject, nil
}

func useCaseOrDefault(useCase string) string {
	if useCase = strings.TrimSpace(useCase); useCase != "" {
		return useCase
	}
	return DefaultUseCase
}
