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
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	ldv "gbo-demo/ldv-client"
)

// The verwerkingsactiviteiten of the consent register, as named in the GBO
// register. Constants rather than configuration: unlike the sidecar, this
// service is not a generic image, and its processings are the operations its
// own API offers.
const (
	consentGrantActivity  = "https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-toestemming-verlenen/v1"
	consentRevokeActivity = "https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-toestemming-intrekken/v1"
	consentStatusActivity = "https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-toestemming-status/v1"
	consentListActivity   = "https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-toestemming-inzage/v1"

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

// buildRecord assembles a record without writing it, so a mutation and its
// record can go into one transaction. Returns nil when there is no logbook or
// no Betrokkene to name — an operation that touched nobody's data.
func (l *registerLogbook) buildRecord(
	r *http.Request,
	activity, name, subjectRef string,
	start time.Time,
	status int,
	extra map[string]any,
) ([]byte, error) {
	if l == nil || subjectRef == "" {
		return nil, nil
	}
	traceID := ldv.TraceID(r.Context(), r.Header)
	record := ldv.Record{
		TraceID:      traceID,
		SpanID:       ldv.SpanID(),
		ParentSpanID: ldv.ParentSpanFor(r.Header, traceID),
		Name:         name,
		Status:       ldv.StatusFromHTTP(status),
		StartTime:    start,
		EndTime:      time.Now().UTC(),
		Resource:     l.Resource(),
		Attributes: ldv.Attributes(
			activity, subjectRef, ldvSubjectTypePortalSubject,
			l.ForeignProcessor(r), extra,
		),
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("encode LDV record: %w", err)
	}
	return encoded, nil
}

// logConsentOperation records one Dataverwerking that mutated nothing — a
// status confirmation or a citizen's inzage. It goes into the same outbox as
// the mutations, so this service has one spool rather than two.
func (l *registerLogbook) logConsentOperation(
	ctx context.Context,
	store ConsentStore,
	r *http.Request,
	activity, name, subjectRef string,
	start time.Time,
	status int,
	extra map[string]any,
) error {
	record, err := l.buildRecord(r, activity, name, subjectRef, start, status, extra)
	if err != nil || record == nil {
		return err
	}
	if err := store.AppendRecord(ctx, record); err != nil {
		ldv.LogFailure(name, err)
		return err
	}
	return nil
}

// deliverOutbox sends everything waiting to the logbook, oldest first, and
// clears each record the logbook accepts. It stops at the first failure, so
// order is preserved and nothing is skipped.
//
// Delivery is separate from writing on purpose: the record is durable the
// moment the transaction commits, and getting it to the logbook afterwards is
// a matter of when, not whether.
func (l *registerLogbook) deliverOutbox(ctx context.Context, store ConsentStore) error {
	if l == nil {
		return nil
	}
	entries, err := store.PendingRecords(ctx, outboxBatchSize)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var record ldv.Record
		if err := json.Unmarshal(entry.Record, &record); err != nil {
			// Undeliverable forever; clearing it is the only way forward, and
			// it says so rather than disappearing.
			ldv.LogFailure("undeliverable record in outbox", err)
			if err := store.MarkDelivered(ctx, entry.ID); err != nil {
				return err
			}
			continue
		}
		if err := l.Write(ctx, record); err != nil {
			return err
		}
		if err := store.MarkDelivered(ctx, entry.ID); err != nil {
			return err
		}
	}
	return nil
}

// outboxBatchSize bounds one delivery pass.
const outboxBatchSize = 100

// runOutbox drains the outbox on an interval until the context ends.
func (l *registerLogbook) runOutbox(ctx context.Context, store ConsentStore, interval time.Duration) {
	if l == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := l.deliverOutbox(ctx, store); err != nil {
				slog.Warn("LDV outbox delivery failed; will retry", "err", err.Error())
			}
		}
	}
}

// logFailure answers a request whose record the logbook did not confirm.
// Every caller does the same thing, and doing it in one place keeps the
// fail-closed rule from being applied inconsistently across the handlers.
func refuseUnlogged(w http.ResponseWriter) {
	writeJSON(w, http.StatusInternalServerError, map[string]string{
		"error": "the operation could not be logged; refusing it",
	})
}
