// Package bsnk talks to BSNk, the BSN-koppelregister. It is the only adapter
// that sees a plain BSN.
//
// The adapter translates representations and nothing else: it does not decide
// which parties a consent has or what a value means. Those are the core's job.
package bsnk

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/upstream"
)

// maxRelyingParties is the most parties BSNk accepts in one transformation.
const maxRelyingParties = 4

// transformLabels name a transformation in the portal's call log.
var transformLabels = map[consent.Form]string{
	consent.FormIdentity:  "Transform to identities",
	consent.FormPseudonym: "Transform to pseudonyms",
}

// Client implements consent.Identities over HTTP.
type Client struct {
	Base   string
	Caller upstream.Caller
	// Requester is the portal's own OIN, which BSNk makes the polymorphic
	// values for and takes them back from.
	Requester string
	// RequesterKeySetVersion is the version of the portal's keys at BSNk.
	RequesterKeySetVersion int
}

// request is what every request to BSNk carries.
type request struct {
	RequestID string `json:"RequestID"`
	DateTime  string `json:"DateTime"`
	Requester string `json:"Requester"`
}

func (c Client) newRequest() request {
	id := make([]byte, 16)
	_, _ = rand.Read(id) // crypto/rand.Read does not fail on supported platforms
	return request{
		RequestID: "_" + hex.EncodeToString(id),
		DateTime:  time.Now().UTC().Format(time.RFC3339),
		Requester: c.Requester,
	}
}

type relyingParty struct {
	EntityID       string `json:"EntityID"`
	KeySetVersion  int    `json:"KeySetVersion"`
	IdentifierType string `json:"IdentifierType"`
}

// Activate turns a BSN into the polymorphic identity and pseudonym BSNk makes
// for the portal.
func (c Client) Activate(ctx context.Context, bsn consent.BSN) (consent.Polymorphic, error) {
	var activated struct {
		// PolymorphicPseudonym holds the polymorphic identity and the
		// polymorphic pseudonym, in that order.
		PolymorphicPseudonym []string `json:"PolymorphicPseudonym"`
	}
	if _, err := c.Caller.DoPrivate(ctx, "Activate BSN", http.MethodPost, c.Base+"/v2/activate", struct {
		request
		RequesterKeySetVersion int    `json:"RequesterKeySetVersion"`
		BSN                    string `json:"BSN"`
	}{c.newRequest(), c.RequesterKeySetVersion, string(bsn)}, &activated); err != nil {
		return consent.Polymorphic{}, err
	}
	if len(activated.PolymorphicPseudonym) != 2 {
		return consent.Polymorphic{}, fmt.Errorf("got %d polymorphic values, want an identity and a pseudonym", len(activated.PolymorphicPseudonym))
	}
	return consent.Polymorphic{PI: activated.PolymorphicPseudonym[0], PP: activated.PolymorphicPseudonym[1]}, nil
}

// AuthorisedForBSN reads the list of parties that may receive the BSN. The
// list names organisations, not citizens, so the call is shown in full.
func (c Client) AuthorisedForBSN(ctx context.Context) ([]string, error) {
	var list struct {
		AuthorizedOrganization []struct {
			OIN string `json:"OIN"`
		} `json:"AuthorizedOrganization"`
	}
	if _, err := c.Caller.Do(ctx, "Read the BSN authorisation list", http.MethodGet, c.Base+"/v2/bsn-authorisation-list", nil, &list); err != nil {
		return nil, err
	}
	oins := make([]string, len(list.AuthorizedOrganization))
	for i, organisation := range list.AuthorizedOrganization {
		oins[i] = organisation.OIN
	}
	return oins, nil
}

// Transform has BSNk make a value of one form per party, from the portal's
// polymorphic identity for identities and from its polymorphic pseudonym for
// pseudonyms. BSNk takes at most four parties per request, so more parties
// take more requests; any one that fails fails the whole call.
func (c Client) Transform(ctx context.Context, values consent.Polymorphic, form consent.Form, parties []consent.Party) (map[string]string, error) {
	encrypted := make(map[string]string, len(parties))
	for start := 0; start < len(parties); start += maxRelyingParties {
		batch := parties[start:min(start+maxRelyingParties, len(parties))]
		if err := c.transform(ctx, values, form, batch, encrypted); err != nil {
			return nil, err
		}
	}
	return encrypted, nil
}

// transform is one request to BSNk, for at most four parties. It adds a value
// for each party to encrypted, and fails unless BSNk answered with exactly
// the values asked for.
func (c Client) transform(ctx context.Context, values consent.Polymorphic, form consent.Form, parties []consent.Party, encrypted map[string]string) error {
	body := struct {
		request
		PolymorphicIdentity  string         `json:"PolymorphicIdentity,omitempty"`
		PolymorphicPseudonym string         `json:"PolymorphicPseudonym,omitempty"`
		RelyingParty         []relyingParty `json:"RelyingParty"`
	}{request: c.newRequest()}
	switch form {
	case consent.FormIdentity:
		body.PolymorphicIdentity = values.PI
	case consent.FormPseudonym:
		body.PolymorphicPseudonym = values.PP
	default:
		return fmt.Errorf("unknown form %q", form)
	}
	asked := make(map[string]consent.Party, len(parties))
	for _, party := range parties {
		body.RelyingParty = append(body.RelyingParty, relyingParty{EntityID: party.OIN, KeySetVersion: party.KeySetVersion, IdentifierType: string(form)})
		asked[party.OIN] = party
	}

	var transformed struct {
		Encrypted []struct {
			EntityID       string `json:"EntityID"`
			KeySetVersion  int    `json:"KeySetVersion"`
			IdentifierType string `json:"IdentifierType"`
			Value          string `json:"value"`
		} `json:"Encrypted"`
	}
	if _, err := c.Caller.DoPrivate(ctx, transformLabels[form], http.MethodPost, c.Base+"/v2/transform", body, &transformed); err != nil {
		return err
	}
	if len(transformed.Encrypted) != len(parties) {
		return fmt.Errorf("got %d values for %d parties", len(transformed.Encrypted), len(parties))
	}
	for _, e := range transformed.Encrypted {
		party, ok := asked[e.EntityID]
		if !ok || e.KeySetVersion != party.KeySetVersion || e.IdentifierType != string(form) || e.Value == "" {
			return fmt.Errorf("got a value that was not asked for: %s, %d, %q", e.EntityID, e.KeySetVersion, e.IdentifierType)
		}
		delete(asked, e.EntityID)
		encrypted[e.EntityID] = e.Value
	}
	return nil
}
