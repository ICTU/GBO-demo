package consent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// After a provider pushed its request, the citizen's browser arrives at the
// portal with the provider's client ID and the request URI, and nothing
// else. The portal resolves the reference for the logged-in citizen, shows
// what the registration says, and records the citizen's decision. The
// decision sends the browser back to the provider with a one-time code, or
// with an error; the provider exchanges the code over FSC for the consent
// token. The token never passes through the browser.

// The refusals of a reference, each a ReasonCode of its own. The citizen is
// shown one text for all of them; the codes are for the logs and the tests.
const (
	// ReasonRequestURIUnknown: the portal does not know the reference.
	ReasonRequestURIUnknown ReasonCode = "REQUEST_URI_UNKNOWN"
	// ReasonRequestURIExpired: the reference outlived its lifetime.
	ReasonRequestURIExpired ReasonCode = "REQUEST_URI_EXPIRED"
	// ReasonRequestURISpent: a decision has already used the reference.
	ReasonRequestURISpent ReasonCode = "REQUEST_URI_SPENT"
	// ReasonClientIDMismatch: the client ID the browser brought is not the
	// provider that pushed the request.
	ReasonClientIDMismatch ReasonCode = "CLIENT_ID_MISMATCH"
	// ReasonRequestURIOtherCitizen: another citizen resolved the reference
	// first.
	ReasonRequestURIOtherCitizen ReasonCode = "REQUEST_URI_OTHER_CITIZEN"
	// ReasonRequestURINotResolved: a decision on a reference the citizen
	// has not resolved, so whose client ID was never checked.
	ReasonRequestURINotResolved ReasonCode = "REQUEST_URI_NOT_RESOLVED"
)

// DefaultConsentValidity is how long a consent holds when the provider
// pushed no valid_until: the same 90 days the portal's consent screen uses.
const DefaultConsentValidity = 90 * 24 * time.Hour

// ConsentRequest is a resolved reference: what the citizen is asked to
// consent to. The name and purpose come from the provider's registration.
type ConsentRequest struct {
	ClientID   PeerID
	ClientName string
	Service    string
	Purpose    string
	Scopes     []string
	ValidUntil time.Time // zero when the provider asked for no end date
}

// ResolveRequest returns the request behind requestURI for citizen and
// binds the reference to them. It is refused, with a *Refusal, when the
// reference is unknown, expired or spent, when clientID is not the provider
// that pushed it, or when another citizen resolved it first. The same
// citizen may resolve it again, such as after a reload.
func (p *Portal) ResolveRequest(_ context.Context, citizen BSN, requestURI, clientID string) (ConsentRequest, error) {
	if p.Requests == nil {
		return ConsentRequest{}, errors.New("no request store is configured")
	}
	subject, err := p.SubjectRefs.For(citizen)
	if err != nil {
		return ConsentRequest{}, fmt.Errorf("derive portal subject reference: %w", err)
	}
	pending, err := p.Requests.Bind(requestURI, clientID, subject, p.now())
	if err != nil {
		return ConsentRequest{}, err
	}
	verified := pending.Request
	return ConsentRequest{
		ClientID:   verified.Provider,
		ClientName: verified.Name,
		Service:    verified.Service,
		Purpose:    verified.Purpose,
		Scopes:     slices.Clone(verified.Scopes),
		ValidUntil: pending.ValidUntil,
	}, nil
}

// Decision is the citizen's answer to a request: grant consent for Scopes,
// a non-empty subset of the requested data sets, or refuse it.
type Decision struct {
	Grant  bool
	Scopes []string
}

// ErrInvalidDecision is returned for a decision that cannot be carried out,
// such as a grant without data sets. It leaves the reference unused.
var ErrInvalidDecision = errors.New("invalid decision")

// Outcome is how a decision ends for the provider.
type Outcome string

const (
	// OutcomeGranted: the consent is given; the provider gets a code.
	OutcomeGranted Outcome = "granted"
	// OutcomeDenied: the citizen refused; the provider gets access_denied.
	OutcomeDenied Outcome = "denied"
	// OutcomeFailed: the consent could not be given; the provider gets
	// server_error.
	OutcomeFailed Outcome = "failed"
)

// Decided is the result of a decision: where the citizen's browser goes
// back to and with what. It carries no consent token: the provider gets
// that only by exchanging Code.
type Decided struct {
	Outcome    Outcome
	ClientName string
	ReturnURL  string // the provider's registered return location
	State      string // the provider's state, returned unchanged
	Code       string // only with OutcomeGranted
	// Failure is why the outcome is OutcomeFailed, for the logs. The
	// citizen and the provider are not told.
	Failure error
}

// Decide records citizen's decision on the request behind requestURI. A
// decision that is not valid for the request returns an error wrapping
// ErrInvalidDecision and leaves the reference as it was; a reference that
// cannot be used is refused as by ResolveRequest. No client ID is checked
// here: a decision is accepted only on a reference this citizen resolved
// first, which checked it, and is refused with ReasonRequestURINotResolved
// otherwise. Any
// other decision spends the reference, and only one can: a refusal gives
// OutcomeDenied, a grant gives the consent and a code (OutcomeGranted) or,
// when the consent could not be given, OutcomeFailed.
func (p *Portal) Decide(ctx context.Context, citizen BSN, requestURI string, decision Decision) (Decided, error) {
	if p.Requests == nil {
		return Decided{}, errors.New("no request store is configured")
	}
	// Checked here, before the reference is spent and the consent given, so
	// that issuing the code below cannot fail for want of a store.
	if p.Codes == nil {
		return Decided{}, errors.New("no code store is configured")
	}
	if decision.Grant {
		if len(decision.Scopes) == 0 || slices.Contains(decision.Scopes, "") {
			return Decided{}, fmt.Errorf("%w: a grant names the data sets it consents to", ErrInvalidDecision)
		}
		if repeated := firstRepeated(decision.Scopes); repeated != "" {
			return Decided{}, fmt.Errorf("%w: data set %q is named more than once", ErrInvalidDecision, repeated)
		}
	}
	subject, err := p.SubjectRefs.For(citizen)
	if err != nil {
		return Decided{}, fmt.Errorf("derive portal subject reference: %w", err)
	}
	now := p.now()
	pending, err := p.Requests.Spend(requestURI, subject, now, func(pending PendingRequest) error {
		if !decision.Grant {
			return nil
		}
		for _, scope := range decision.Scopes {
			if !slices.Contains(pending.Request.Scopes, scope) {
				return fmt.Errorf("%w: data set %q was not requested", ErrInvalidDecision, scope)
			}
		}
		return nil
	})
	if err != nil {
		return Decided{}, err
	}

	verified := pending.Request
	decided := Decided{ClientName: verified.Name, ReturnURL: verified.ReturnURL, State: pending.State}
	if !decision.Grant {
		decided.Outcome = OutcomeDenied
		return decided, nil
	}

	validity := DefaultConsentValidity
	if !pending.ValidUntil.IsZero() {
		validity = pending.ValidUntil.Sub(now)
	}
	scopes := slices.Clone(decision.Scopes)
	granted, err := p.GiveConsent(ctx, citizen, GiveInput{
		DienstverlenerOIN: string(verified.Provider),
		Scopes:            scopes,
		ValiditySeconds:   max(1, int(validity.Seconds())),
		UseCase:           verified.Purpose,
	})
	if err == nil {
		// Once the consent is given, issuing its code cannot fail: the store
		// is checked above and crypto/rand does not return errors (Go 1.24
		// and later). So no rollback of the consent is built; were Issue to
		// fail, the citizen would see an active consent the provider never
		// got a token for, and could revoke it in the portal.
		decided.Code, err = p.issueCode(IssuedCode{
			Provider:      verified.Provider,
			RedirectURI:   verified.ReturnURL,
			CodeChallenge: pending.CodeChallenge,
			Consent:       granted,
			Scopes:        scopes,
		})
	}
	if err != nil {
		decided.Outcome, decided.Failure = OutcomeFailed, err
		return decided, nil
	}
	decided.Outcome = OutcomeGranted
	return decided, nil
}

// firstRepeated returns the first data set that occurs more than once in
// scopes, or "".
func firstRepeated(scopes []string) string {
	seen := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if seen[scope] {
			return scope
		}
		seen[scope] = true
	}
	return ""
}
