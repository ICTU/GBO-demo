// LDV wiring specific to the EUDI adapter: which parts of an issuance are
// Dataverwerkingen of the GBO voorziening, and about whom.
//
// The generic client lives in ldv.go and the subject derivation in
// ldv_subject.go; this file is what the adapter itself knows.
package main

import (
	"context"
	ldv "gbo-demo/ldv-client"
	"net/http"
	"time"
)

// The verwerkingsactiviteiten of the adapter, as named in GBO's register.
// Constants rather than configuration: this service is not a generic image,
// and its processings are the two steps of an issuance it actually performs.
const (
	pidExtractionActivity    = "gbo-pid-bsn-extractie@v1"
	attestationBuildActivity = "gbo-attestatie-samenstellen@v1"
)

// issuanceLogbook is the adapter's view of GBO's logbook: the shared client,
// plus what only this service knows — the two steps of an issuance that are
// Dataverwerkingen of the voorziening.
//
// A nil *issuanceLogbook means the adapter is not part of an LDV chain, so the
// methods below are nil-safe and the handler needs no branch.
type issuanceLogbook struct {
	*ldv.Client
}

// newIssuanceLogbook wraps a client, or returns nil when there is none.
func newIssuanceLogbook(client *ldv.Client) *issuanceLogbook {
	if client == nil {
		return nil
	}
	return &issuanceLogbook{Client: client}
}

// issuanceRecording carries the identity of an issuance's records while the
// request is still running, so the second record can hang under the first.
type issuanceRecording struct {
	traceID     string
	extractSpan string
	subjectID   string
	subjectType string
	processor   string
}

// ldvTraceIDForIssuance returns the trace id the whole chain will share.
//
// The adapter mints the Fsc-Transaction-Id itself and stashes it on the
// context, so it is taken from there rather than from a header: when the
// issuance-server sends its own traceparent the OTel trace id no longer
// equals the FSC id, and reading the ambient trace would file the adapter's
// records under an id the source's logbook never sees.
func ldvTraceIDForIssuance(ctx context.Context, header http.Header) string {
	if fscTxID, ok := ctx.Value(fscTxIDCtxKey).(string); ok && fscTxID != "" {
		if normalized := ldv.NormalizeTraceID(fscTxID); normalized != "" {
			return normalized
		}
	}
	return ldv.TraceID(ctx, header)
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
	subjectID, subjectType := l.Subject(r.Header, bsn)
	recording := issuanceRecording{
		traceID:     ldvTraceIDForIssuance(ctx, r.Header),
		extractSpan: ldv.SpanID(),
		subjectID:   subjectID,
		subjectType: subjectType,
		processor:   ldv.ForeignProcessor(r),
	}
	record := ldv.Record{
		TraceID:      recording.traceID,
		SpanID:       recording.extractSpan,
		ParentSpanID: ldv.ParentSpanFromHeader(r.Header),
		Name:         "dataverwerking.pid-bsn-extractie",
		Status:       "OK",
		StartTime:    start,
		EndTime:      time.Now().UTC(),
		Attributes: ldv.Attributes(pidExtractionActivity, subjectID, subjectType, recording.processor, map[string]any{
			"gbo.source_id": sourceID,
			"gbo.type_id":   typeID,
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
		SpanID:       ldv.SpanID(),
		ParentSpanID: recording.extractSpan,
		Name:         "dataverwerking.attestatie-samenstellen",
		Status:       "OK",
		StartTime:    start,
		EndTime:      time.Now().UTC(),
		Attributes: ldv.Attributes(attestationBuildActivity, recording.subjectID, recording.subjectType, recording.processor, map[string]any{
			"gbo.source_id":  sourceID,
			"gbo.source_oin": sourceOIN,
			"gbo.type_id":    typeID,
			// How many claims ended up in the attestation, not which: the
			// record says what was processed, it is not a copy of it.
			"gbo.attestatie.claims": claims,
		}),
	}
	if err := l.Write(ctx, record); err != nil {
		ldv.LogFailure(record.Name, err)
		return err
	}
	return nil
}
