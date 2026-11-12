package consent

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// The checks a request to start a consent flow passes before any citizen
// sees it. They run on what the service provider claims, against what the
// DvTP admission register says about that provider. The provider is the
// party the transport authenticated (its FSC peer ID), never a field of the
// request.

// PeerID is a party's FSC peer ID: the OIN from its certificate.
type PeerID string

// ReasonCode says why a request to start a consent flow was refused.
type ReasonCode string

const (
	// ReasonInitiationIncomplete: a required field is missing.
	ReasonInitiationIncomplete ReasonCode = "INITIATION_INCOMPLETE"
	// ReasonPartyNotActive: the provider is not an admitted, active
	// participant of the admission register.
	ReasonPartyNotActive ReasonCode = "PARTY_NOT_ACTIVE"
	// ReasonInitiationMismatch: the request does not match the provider's
	// registration: a client ID other than the authenticated provider, a
	// service the provider has not registered, or a name or purpose that
	// differs from the registration.
	ReasonInitiationMismatch ReasonCode = "INITIATION_MISMATCH"
	// ReasonUnknownScope: a requested data set is not one the portal knows.
	ReasonUnknownScope ReasonCode = "UNKNOWN_SCOPE"
	// ReasonReturnURLNotRegistered: the return URL is not one of the
	// provider's registered return locations.
	ReasonReturnURLNotRegistered ReasonCode = "RETURN_URL_NOT_REGISTERED"
	// ReasonAdmissionRegisterUnavailable: the admission register could not
	// be read, so nothing could be checked. The request is refused: the
	// checks fail closed. The PDP's autosign policy uses the same code.
	ReasonAdmissionRegisterUnavailable ReasonCode = "ADMISSION_REGISTER_UNAVAILABLE"
)

// Refusal is a request the checks refused, with the reason code the provider
// receives. Detail is for the provider and the logs. Err is the underlying
// failure when the register could not be read.
type Refusal struct {
	Code   ReasonCode
	Detail string
	Err    error
}

func (r *Refusal) Error() string {
	if r.Err != nil {
		return fmt.Sprintf("%s: %s: %v", r.Code, r.Detail, r.Err)
	}
	return fmt.Sprintf("%s: %s", r.Code, r.Detail)
}

func (r *Refusal) Unwrap() error { return r.Err }

func refuse(code ReasonCode, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// ErrUnknownParty is returned by Registrations when the admission register
// does not know the peer at all.
var ErrUnknownParty = errors.New("party unknown to the admission register")

// RegisteredService is a service a provider asks consent for, with the
// purpose the citizen is shown.
type RegisteredService struct {
	Service string
	Purpose string
}

// Registration is a participant of the admission register as the checks
// see it: its name, whether it is admitted and active, and what it has
// registered as a service provider. A participant without a registration
// has no services and no return locations.
type Registration struct {
	Name            string
	Active          bool
	Services        []RegisteredService
	ReturnLocations []string
}

// ClaimedRequest is what a service provider asks for when it starts a
// consent flow. Name and Purpose are optional; when present they must
// match the registration.
type ClaimedRequest struct {
	ClientID  string
	Service   string
	Scopes    []string
	ReturnURL string
	Name      string
	Purpose   string
}

// VerifiedRequest is a request that passed every check. Name and Purpose
// come from the registration, not from the request: they are what the
// citizen is shown.
type VerifiedRequest struct {
	Provider  PeerID
	Name      string
	Service   string
	Purpose   string
	Scopes    []string
	ReturnURL string
}

// VerifyRequest checks a request to start a consent flow from provider, the
// authenticated peer, against the admission register. It returns the
// verified request, or a *Refusal with the reason code of the first check
// that failed, in this order: a required field is missing, the provider is
// not active, the request does not match the registration, a data set is
// unknown, the return URL is not registered. When the register cannot be
// read the request is refused with ReasonAdmissionRegisterUnavailable.
//
// The return URL must equal a registered return location exactly, with no
// normalisation and no prefix match; whatever context the provider needs
// back belongs in its state parameter.
func (p *Portal) VerifyRequest(ctx context.Context, provider PeerID, claimed ClaimedRequest) (VerifiedRequest, error) {
	if missing := claimed.missing(); missing != "" {
		return VerifiedRequest{}, refuse(ReasonInitiationIncomplete, "the request has no %s", missing)
	}

	if p.Registrations == nil {
		return VerifiedRequest{}, refuse(ReasonAdmissionRegisterUnavailable, "no admission register is configured")
	}
	registration, err := p.Registrations.Registration(ctx, provider)
	switch {
	case errors.Is(err, ErrUnknownParty):
		return VerifiedRequest{}, refuse(ReasonPartyNotActive, "%s is not a participant of the admission register", provider)
	case err != nil:
		return VerifiedRequest{}, &Refusal{Code: ReasonAdmissionRegisterUnavailable, Detail: "the admission register could not be read", Err: err}
	case !registration.Active:
		return VerifiedRequest{}, refuse(ReasonPartyNotActive, "%s is not active in the admission register", provider)
	}

	if claimed.ClientID != string(provider) {
		return VerifiedRequest{}, refuse(ReasonInitiationMismatch, "client_id %q is not the requesting peer %s", claimed.ClientID, provider)
	}
	service, registered := registration.service(claimed.Service)
	if !registered {
		return VerifiedRequest{}, refuse(ReasonInitiationMismatch, "service %q is not registered for %s", claimed.Service, provider)
	}
	if claimed.Name != "" && claimed.Name != registration.Name {
		return VerifiedRequest{}, refuse(ReasonInitiationMismatch, "the name differs from the registration of %s", provider)
	}
	if claimed.Purpose != "" && claimed.Purpose != service.Purpose {
		return VerifiedRequest{}, refuse(ReasonInitiationMismatch, "the purpose differs from the registration of service %q", service.Service)
	}

	for _, scope := range claimed.Scopes {
		if !slices.Contains(p.KnownScopes, scope) {
			return VerifiedRequest{}, refuse(ReasonUnknownScope, "data set %q is unknown", scope)
		}
	}

	if !slices.Contains(registration.ReturnLocations, claimed.ReturnURL) {
		return VerifiedRequest{}, refuse(ReasonReturnURLNotRegistered, "the return URL is not registered for %s", provider)
	}

	return VerifiedRequest{
		Provider:  provider,
		Name:      registration.Name,
		Service:   service.Service,
		Purpose:   service.Purpose,
		Scopes:    slices.Clone(claimed.Scopes),
		ReturnURL: claimed.ReturnURL,
	}, nil
}

// missing names the first required field the request lacks, or "".
func (c ClaimedRequest) missing() string {
	switch {
	case c.ClientID == "":
		return "client_id"
	case c.Service == "":
		return "service"
	case len(c.Scopes) == 0 || slices.Contains(c.Scopes, ""):
		return "data sets"
	case c.ReturnURL == "":
		return "return URL"
	}
	return ""
}

func (r Registration) service(name string) (RegisteredService, bool) {
	for _, service := range r.Services {
		if service.Service == name {
			return service, true
		}
	}
	return RegisteredService{}, false
}
