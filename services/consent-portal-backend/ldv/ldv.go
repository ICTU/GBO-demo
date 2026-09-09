// Package ldv is the driven adapter for the Logboek Dataverwerkingen: it
// translates the core's Processing into the record the standard defines and
// hands it to the shared client.
//
// It used to speak HTTP itself. That made it a second implementation of one
// wire contract, free to drift from the one every other component uses — and
// it did, on exactly the fields the standard is specific about. Translation is
// this package's job; the format is the client's.
package ldv

import (
	"context"

	ldvclient "gbo-demo/ldv-client"

	"gbo-demo/consent-portal-backend/consent"
)

// subjectTypePortalSubject is the portal-scoped reference: what this side of
// the chain names a Betrokkene by. Not the BSN it started from and not the PI
// it derives for the dienstverlener; data_subject_id_type is what keeps those
// apart for whoever reads the record later.
const subjectTypePortalSubject = "portal-subject"

// Logbook implements consent.Logbook over the shared client.
type Logbook struct {
	client *ldvclient.Client
}

// New wires the adapter, or returns nil when this portal is not part of an LDV
// chain. The nil is deliberate and load-bearing: the core treats a nil Logbook
// as "no chain" and writes nothing.
func New(serviceName, logbookURL, token string) (*Logbook, *ldvclient.Client, error) {
	client, err := ldvclient.New(ldvclient.Config{
		ServiceName: serviceName,
		LogbookURL:  logbookURL,
		WriteToken:  token,
		// No pseudonym key: this portal never puts a BSN in a record. It
		// names the Betrokkene by the portal-scoped reference it derived,
		// which is the only identifier it is allowed to write down.
	})
	if err != nil || client == nil {
		return nil, nil, err
	}
	return &Logbook{client: client}, client, nil
}

// Record writes one Dataverwerking. With a spool attached the record is made
// durable locally and delivered afterwards, so a citizen's action never waits
// on the logbook — and never completes without a record either.
func (l *Logbook) Record(ctx context.Context, processing consent.Processing) error {
	attributes := ldvclient.Attributes(
		processing.Activity,
		string(processing.Subject),
		subjectTypePortalSubject,
		// No foreign operation processor: the citizen is the caller, so
		// there is no other application to name.
		"",
		processing.Attributes,
	)
	status := ldvclient.StatusOK
	if processing.Failed {
		status = ldvclient.StatusError
	}
	return l.client.Write(ctx, ldvclient.Record{
		// The citizen calls this portal directly, so there is no
		// Fsc-Transaction-Id yet; the ambient OTel trace is the correlation
		// handle, and the FSC hop happens later, downstream of the consent.
		TraceID:    ldvclient.TraceID(ctx, nil),
		SpanID:     ldvclient.SpanID(),
		Name:       processing.Name,
		StartTime:  processing.Start,
		EndTime:    processing.End,
		Status:     status,
		Attributes: attributes,
	})
}
