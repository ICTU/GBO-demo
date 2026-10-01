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

// identifierIdentity is BSNk's name for a value that decrypts to the BSN.
const identifierIdentity = "Identity"

// Client implements consent.Identities and consent.Pseudonymizer over HTTP.
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

// EncryptFor activates the BSN and has BSNk transform the polymorphic
// identity into an encrypted identity per party. The polymorphic identity is
// usable by the portal alone and does not outlive this call.
func (c Client) EncryptFor(ctx context.Context, bsn consent.BSN, parties []consent.Party) ([]consent.EncryptedSubject, error) {
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
		return nil, fmt.Errorf("activate: %w", err)
	}
	if len(activated.PolymorphicPseudonym) != 2 {
		return nil, fmt.Errorf("activate: got %d polymorphic values, want an identity and a pseudonym", len(activated.PolymorphicPseudonym))
	}

	relyingParties := make([]relyingParty, len(parties))
	for i, party := range parties {
		relyingParties[i] = relyingParty{EntityID: party.OIN, KeySetVersion: party.KeySetVersion, IdentifierType: identifierIdentity}
	}
	var transformed struct {
		Encrypted []struct {
			EntityID       string `json:"EntityID"`
			KeySetVersion  int    `json:"KeySetVersion"`
			IdentifierType string `json:"IdentifierType"`
			Value          string `json:"value"`
		} `json:"Encrypted"`
	}
	if _, err := c.Caller.DoPrivate(ctx, "Transform for the sources", http.MethodPost, c.Base+"/v2/transform", struct {
		request
		PolymorphicIdentity string         `json:"PolymorphicIdentity"`
		RelyingParty        []relyingParty `json:"RelyingParty"`
	}{c.newRequest(), activated.PolymorphicPseudonym[0], relyingParties}, &transformed); err != nil {
		return nil, fmt.Errorf("transform: %w", err)
	}
	if len(transformed.Encrypted) != len(parties) {
		return nil, fmt.Errorf("transform: got %d values for %d parties", len(transformed.Encrypted), len(parties))
	}

	subjects := make([]consent.EncryptedSubject, len(transformed.Encrypted))
	for i, e := range transformed.Encrypted {
		subjects[i] = consent.EncryptedSubject{
			Party:          consent.Party{OIN: e.EntityID, KeySetVersion: e.KeySetVersion},
			IdentifierType: e.IdentifierType,
			Value:          e.Value,
		}
	}
	return subjects, nil
}

// Pseudonymize derives the portal's own reference to a citizen.
func (c Client) Pseudonymize(ctx context.Context, bsn consent.BSN, recipientOIN string) (string, error) {
	var out struct {
		Pseudonym string `json:"pseudonym"`
	}
	_, err := c.Caller.DoPrivate(ctx, "Pseudonymize BSN", http.MethodPost, c.Base+"/pseudonymize",
		map[string]any{"bsn": string(bsn), "recipient_oin": recipientOIN}, &out)
	if err != nil {
		return "", err
	}
	return out.Pseudonym, nil
}
