// LDV wiring specific to the consent register: which of its operations are
// Dataverwerkingen of the GBO voorziening, and about whom.
//
// The generic client lives in ldv.go. This service never sees a BSN — the
// portal pseudonymises before anything reaches here — so it carries no
// subject-derivation file: it names the Betrokkene by the portal-scoped
// reference it already works with.
package main

import (
	"context"
	ldv "gbo-demo/ldv-client"
	"net/http"
	"time"
)

// The verwerkingsactiviteiten of the consent register, as named in the GBO
// register. Constants rather than configuration: unlike the sidecar, this
// service is not a generic image, and its processings are the operations its
// own API offers.
const (
	consentGrantActivity  = "gbo-toestemming-verlenen@v1"
	consentRevokeActivity = "gbo-toestemming-intrekken@v1"
	consentStatusActivity = "gbo-toestemming-status@v1"
	consentListActivity   = "gbo-toestemming-inzage@v1"

	// The portal-scoped subject reference. The register holds nothing else:
	// no BSN, and deliberately no PI either — the PI travels only inside the
	// signed consent token, so the reference that names a Betrokkene here is
	// not the one the source chain works with. `data_subject_id_type` is what
	// makes that difference explicit rather than confusing (REQ-72).
	ldvSubjectTypePortalSubject = "portal-subject"
)

// registerLogbook is this service's view of GBO's logbook: the shared client,
// plus what only the consent register knows — which of its operations are
// Dataverwerkingen and whose data they touch.
//
// A nil *registerLogbook means the register is not part of an LDV chain, so
// the method below is nil-safe and the handlers need no branch.
type registerLogbook struct {
	*ldv.Client
}

// newRegisterLogbook wraps a client, or returns nil when there is none.
func newRegisterLogbook(client *ldv.Client) *registerLogbook {
	if client == nil {
		return nil
	}
	return &registerLogbook{Client: client}
}

// logConsentOperation records one Dataverwerking of the register.
//
// subjectRef is the portal-scoped reference of the Betrokkene whose consent
// this is. An operation that resolved no consent — a status query for an id
// that does not exist — touched nobody's personal data and writes no record;
// the caller passes an empty reference for that.
//
// The error is meant to be propagated: an operation that cannot be logged
// must fail rather than complete unlogged.
func (l *registerLogbook) logConsentOperation(
	ctx context.Context,
	r *http.Request,
	activity, name, subjectRef string,
	start time.Time,
	status int,
	extra map[string]any,
) error {
	if l == nil || subjectRef == "" {
		return nil
	}
	record := ldv.Record{
		TraceID:      ldv.TraceID(ctx, r.Header),
		SpanID:       ldv.SpanID(),
		ParentSpanID: ldv.ParentSpanFromHeader(r.Header),
		Name:         name,
		Status:       ldv.StatusFromHTTP(status),
		StartTime:    start,
		EndTime:      time.Now().UTC(),
		Attributes: ldv.Attributes(
			activity,
			subjectRef, ldvSubjectTypePortalSubject,
			ldv.ForeignProcessor(r), extra,
		),
	}
	if err := l.Write(ctx, record); err != nil {
		ldv.LogFailure(record.Name, err)
		return err
	}
	return nil
}

// logFailure answers a request whose record the logbook did not confirm.
// Every caller does the same thing, and doing it in one place keeps the
// fail-closed rule from being applied inconsistently across the handlers.
func refuseUnlogged(w http.ResponseWriter) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error": "the operation could not be logged; refusing it",
	})
}
