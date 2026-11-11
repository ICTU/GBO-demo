package issuance

import (
	"context"
	"slices"
	"strings"

	"gbo-demo/oid4vc-kit/dpop"
	"gbo-demo/oid4vc-kit/oid4vci"
)

// NotificationCall is a notification request with its authorization.
type NotificationCall struct {
	Target        Target
	Authorization string
	DPoP          []string
	Notification  oid4vci.Notification
}

// Notify takes a wallet's notification about an issued credential (section
// 11) on the eudi profile. The issuer acts on none of the events (N1): the
// caller logs the event. A notification id is good for one notification of
// the session it was issued in.
func (s *Service) Notify(ctx context.Context, call NotificationCall) error {
	_, p, err := s.resolve(call.Target)
	if err != nil {
		return err
	}
	if p.Kind != KindEUDI {
		return ErrUnknownProfile
	}
	now := s.now()
	token, session, err := s.authorize(ctx, call.Target, call.Authorization, "DPoP")
	if err != nil {
		return err
	}
	accessToken := strings.TrimSpace(call.Authorization[len("DPoP"):])
	proof, err := dpop.Verify(call.DPoP, dpop.Expected{Method: "POST", URL: s.Identifier(call.Target) + "/notification", AccessToken: accessToken}, now)
	if err != nil {
		return refuse(oid4vci.ErrInvalidDPoPProof, err.Error())
	}
	if proof.JKT != token.JKT {
		return refuse(oid4vci.ErrInvalidDPoPProof, "the DPoP key is not the key the access token is bound to")
	}
	if ok, err := s.onetime.FirstUse(ctx, kindDPoP, proof.JTI, proof.IAT.Add(dpop.Window), now); err != nil {
		return err
	} else if !ok {
		return refuse(oid4vci.ErrInvalidDPoPProof, "the DPoP proof has been used before")
	}
	n := call.Notification
	events := []string{oid4vci.EventCredentialAccepted, oid4vci.EventCredentialFailure, oid4vci.EventCredentialDeleted}
	if n.NotificationID == "" || !slices.Contains(events, n.Event) {
		return refuse(oid4vci.ErrInvalidNotificationRequest, "notification_id and an event of section 11.1 are required")
	}
	if ok, err := s.onetime.Take(ctx, scoped(kindNotification, session.ID), n.NotificationID, now); err != nil {
		return err
	} else if !ok {
		return refuse(oid4vci.ErrInvalidNotificationID, "the notification_id is unknown, used or of another issuance")
	}
	return nil
}
