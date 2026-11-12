// Package onboarding reads the DvTP admission register
// (dvtp-onboarding-register): who a participant is, whether it is active,
// and what it registered as a service provider.
//
// The adapter translates representations; it does not decide outcomes.
// Whether a request passes is decided in the consent package.
package onboarding

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/upstream"
)

// Client implements consent.Registrations over the register's internal
// endpoint for the consent portal.
type Client struct {
	Base   string
	Caller upstream.Caller
}

type participant struct {
	PeerID   string `json:"peer_id"`
	Name     string `json:"name"`
	Active   bool   `json:"active"`
	Services []struct {
		Service string `json:"service"`
		Purpose string `json:"purpose"`
	} `json:"services"`
	ReturnLocations []string `json:"return_locations"`
}

// Registration reads one participant. A 404 is a peer the register does not
// know; every other failure, including an answer that does not decode or is
// not about the requested peer (such as `null` or `{}`), means the register
// could not be read.
func (c Client) Registration(ctx context.Context, peer consent.PeerID) (consent.Registration, error) {
	var out participant
	status, err := c.Caller.Do(ctx, "Read Registration", http.MethodGet,
		c.Base+"/internal/consent-portal/participants/"+url.PathEscape(string(peer)), nil, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return consent.Registration{}, fmt.Errorf("%s: %w", peer, consent.ErrUnknownParty)
		}
		return consent.Registration{}, err
	}
	if out.PeerID != string(peer) {
		return consent.Registration{}, fmt.Errorf("the register answered for peer %q, not for %s", out.PeerID, peer)
	}
	registration := consent.Registration{
		Name:            out.Name,
		Active:          out.Active,
		ReturnLocations: out.ReturnLocations,
	}
	for _, service := range out.Services {
		registration.Services = append(registration.Services, consent.RegisteredService{
			Service: service.Service,
			Purpose: service.Purpose,
		})
	}
	return registration, nil
}
