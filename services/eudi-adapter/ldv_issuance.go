// LDV wiring specific to the EUDI adapter: which parts of an issuance are
// Dataverwerkingen of the GBO voorziening, and about whom.
//
// The generic client lives in ldv.go and the subject derivation in
// ldv_subject.go; this file is what the adapter itself knows.
package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	ldv "gbo-demo/ldv-client"
)

// The verwerkingsactiviteiten of the adapter, as named in GBO's register.
// Constants rather than configuration: this service is not a generic image,
// and its processings are the two steps of an issuance it actually performs.
const (
	pidExtractionActivity    = "https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-pid-bsn-extractie/v1"
	attestationBuildActivity = "https://logboek.gbo.overheid.nl/verwerkingsactiviteiten/gbo-attestatie-samenstellen/v1"
)

// issuanceLogbook is the adapter's view of GBO's logbook: the shared client,
// plus what only this service knows — the two steps of an issuance that are
// Dataverwerkingen of the voorziening.
//
// A nil *issuanceLogbook means the adapter is not part of an LDV chain, so the
// methods below are nil-safe and the handler needs no branch.
type issuanceLogbook struct {
	*ldv.Client
	// nextLogbooks maps a source id onto the read-API URI of the logbook that
	// holds the bronhouder's half of the request. The extension defines
	// dpl.read.nextLogbookId as "uri naar uniek identificeerbare API volgens
	// extensie lezen", so a reader can follow it rather than having to know
	// what a local name like "logboek-bd" stands for.
	nextLogbooks map[string]string
}

// newIssuanceLogbook wraps a client, or returns nil when there is none.
func newIssuanceLogbook(client *ldv.Client, nextLogbooks map[string]string) *issuanceLogbook {
	if client == nil {
		return nil
	}
	return &issuanceLogbook{Client: client, nextLogbooks: nextLogbooks}
}

// parseNextLogbooks reads a "sourceID=logbookID,sourceID=logbookID" mapping.
// A malformed entry is skipped rather than fatal: a missing pointer costs a
// reader one manual hop, while refusing to start costs every issuance.
func parseNextLogbooks(raw string) map[string]string {
	mapping := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		sourceID, logbookID, found := strings.Cut(strings.TrimSpace(entry), "=")
		if !found {
			continue
		}
		if sourceID, logbookID = strings.TrimSpace(sourceID), strings.TrimSpace(logbookID); sourceID != "" && logbookID != "" {
			mapping[sourceID] = logbookID
		}
	}
	return mapping
}

// issuanceRecording carries the identity of an issuance's records while the
// request is still running, so the second record can hang under the first.
type issuanceRecording struct {
	traceID      string
	extractSpan  string
	assemblySpan string
	subjectID    string
	subjectType  string
	processor    string
}

// sourceCall is where the source call sits in the trace: under the assembly
// record, whose nextLogbookId points at the bronhouder's logbook. That span is
// minted together with the extraction so it exists before the call is made;
// the record itself is written once the attestation is assembled.
func (r issuanceRecording) sourceCall() ldv.TraceContext {
	return ldv.TraceContext{TraceID: r.traceID, SpanID: r.assemblySpan, Sampled: true}
}

// sourceCallKey carries that position on the context, so callViaFSC can hand
// it to the bronhouder without the recording being threaded through every
// transport.
type sourceCallKey struct{}

func contextWithSourceCall(ctx context.Context, recording issuanceRecording) context.Context {
	return context.WithValue(ctx, sourceCallKey{}, recording.sourceCall())
}

// sourceCallFrom returns the position set by contextWithSourceCall. Without a
// logbook there is none, and the traceparent OTel injects is what travels.
func sourceCallFrom(ctx context.Context) (ldv.TraceContext, bool) {
	traceContext, ok := ctx.Value(sourceCallKey{}).(ldv.TraceContext)
	return traceContext, ok
}

// logPIDExtraction records reading the BSN out of the disclosed PID — the
// step that establishes who the wallet holder is, and the reason every later
// step in this request touches this person's data.
//
// The record names the Betrokkene by a logbook-local pseudonym: the adapter
// holds a BSN and nothing else, and a BSN may not appear in a record
// (REQ-60/72). That it has nothing better to use is the gap this makes
// visible.
func (l *issuanceLogbook) logPIDExtraction(ctx context.Context, r *http.Request, bsn string, start time.Time, sourceID, typeID string) (issuanceRecording, error) {
	if l == nil {
		return issuanceRecording{}, nil
	}
	subjectID, subjectType, err := l.Subject(r.Header, bsn)
	if err != nil {
		ldv.LogFailure("dataverwerking.pid-bsn-extractie", err)
		return issuanceRecording{}, err
	}
	recording := issuanceRecording{
		// The request's own trace: the caller's traceparent when it sent one,
		// otherwise the one withFscTraceContext tied to the transaction id.
		traceID:      ldv.TraceID(ctx, r.Header),
		extractSpan:  ldv.SpanID(),
		assemblySpan: ldv.SpanID(),
		subjectID:    subjectID,
		subjectType:  subjectType,
		processor:    l.ForeignProcessor(r),
	}
	record := ldv.Record{
		TraceID:      recording.traceID,
		SpanID:       recording.extractSpan,
		ParentSpanID: ldv.ParentSpanFor(r.Header, recording.traceID),
		Name:         "dataverwerking.pid-bsn-extractie",
		Status:       "OK",
		StartTime:    start,
		EndTime:      time.Now().UTC(),
		Attributes: ldv.Attributes(pidExtractionActivity, subjectID, subjectType, recording.processor, map[string]any{
			"dpl.gbo.sourceId": sourceID,
			"dpl.gbo.typeId":   typeID,
		}),
	}
	if err := l.Write(ctx, record); err != nil {
		ldv.LogFailure(record.Name, err)
		return issuanceRecording{}, err
	}
	return recording, nil
}

// logAttestationAssembly records turning the source's answer into the
// attestation that goes to the wallet. It hangs under the extraction record:
// the assembly exists only because that step established whose attestation
// this is.
func (l *issuanceLogbook) logAttestationAssembly(ctx context.Context, recording issuanceRecording, start time.Time, sourceID, typeID, sourceOIN string, claims int) error {
	if l == nil {
		return nil
	}
	record := ldv.Record{
		TraceID:      recording.traceID,
		SpanID:       recording.assemblySpan,
		ParentSpanID: recording.extractSpan,
		Name:         "dataverwerking.attestatie-samenstellen",
		Status:       "OK",
		StartTime:    start,
		EndTime:      time.Now().UTC(),
		Attributes: ldv.Attributes(attestationBuildActivity, recording.subjectID, recording.subjectType, recording.processor, map[string]any{
			// Where this processing continues: the bronhouder logged its own
			// half of this request under the same trace id.
			ldv.AttrNextLogbookID: l.nextLogbooks[sourceID],
			"dpl.gbo.sourceId":    sourceID,
			"dpl.gbo.typeId":      typeID,
			// How many claims ended up in the attestation, not which: the
			// record says what was processed, it is not a copy of it.
			"dpl.gbo.attestatieClaims": claims,
		}),
	}
	if err := l.Write(ctx, record); err != nil {
		ldv.LogFailure(record.Name, err)
		return err
	}
	return nil
}
