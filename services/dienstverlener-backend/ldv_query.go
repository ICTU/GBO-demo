// LDV wiring for the consumer: which of Hypotheek-BV's processings is a
// Dataverwerking, and where it continues.
//
// A private party is not bound by LDV. The demo applies it provisionally, in a
// logbook that is Hypotheek-BV's own: the read extension starts a chain at the
// application that started the processing, and without the consumer's records
// a reader cannot know which sources one processing touched.
package main

import (
	"context"
	"net/http"
	"strings"
	"time"

	ldv "gbo-demo/ldv-client"
)

// queryActivity is Hypotheek-BV's verwerkingsactiviteit for asking a source
// for the income data a mortgage assessment needs.
const queryActivity = "https://logboek.hypotheek-bv.test/verwerkingsactiviteiten/hbv-inkomensgegevens-opvragen/v1"

// ldvDeliveryInterval is how often the LDV spool is drained. Records are
// durable the moment they are written, so this governs only how quickly they
// reach the logbook, not whether they do.
const ldvDeliveryInterval = 2 * time.Second

// queryLogbook is the consumer's view of its logbook: the shared client, plus
// where each source keeps its half of a request. A nil *queryLogbook means the
// consumer writes no records, so the methods below are nil-safe.
type queryLogbook struct {
	*ldv.Client
	// nextLogbooks maps a source, named by the prefix of its scopes ("bd" in
	// "bd:ib:2025"), onto the read API of that source's logbook — the value of
	// dpl.read.nextLogbookId. In a deployment it comes from the source's
	// service description.
	nextLogbooks map[string]string
}

// newQueryLogbook wraps a client, or returns nil when there is none.
func newQueryLogbook(client *ldv.Client, nextLogbooks map[string]string) *queryLogbook {
	if client == nil {
		return nil
	}
	return &queryLogbook{Client: client, nextLogbooks: nextLogbooks}
}

// parseNextLogbooks reads a "source=logbookID,source=logbookID" mapping. A
// malformed entry is skipped rather than fatal: a missing pointer costs a
// reader one manual hop, while refusing to start costs every query.
func parseNextLogbooks(raw string) map[string]string {
	mapping := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		source, logbookID, found := strings.Cut(strings.TrimSpace(entry), "=")
		if !found {
			continue
		}
		if source, logbookID = strings.TrimSpace(source), strings.TrimSpace(logbookID); source != "" && logbookID != "" {
			mapping[source] = logbookID
		}
	}
	return mapping
}

// sourceCall is one call to a source while it is in flight. Its record's
// identity is minted before the call, so the traceparent can carry it and the
// source's records hang under it.
type sourceCall struct {
	traceID string
	spanID  string
	start   time.Time
}

// begin opens the record of one source call. The trace is the request's own:
// withFscTraceContext tied it to the transaction id unless a caller brought a
// traceparent. The record has no parent, because the consumer starts the
// chain.
func (l *queryLogbook) begin(ctx context.Context, r *http.Request) sourceCall {
	if l == nil {
		return sourceCall{}
	}
	return sourceCall{traceID: ldv.TraceID(ctx, r.Header), spanID: ldv.SpanID(), start: time.Now().UTC()}
}

// inject puts the call's position on the outgoing request, replacing the
// traceparent OTel injected: the same trace, but a span the source's records
// hang under and a reader of this logbook can resolve.
func (c sourceCall) inject(header http.Header) {
	if c.traceID == "" {
		return
	}
	ldv.InjectTraceparent(header, ldv.TraceContext{TraceID: c.traceID, SpanID: c.spanID, Sampled: true})
}

// logSourceCall records the call. It happened whatever the source answered — a
// refused or failed call is still a Dataverwerking, with status ERROR — so it
// is logged before the answer is used, and a record that cannot be made
// durable withholds the answer.
func (l *queryLogbook) logSourceCall(ctx context.Context, call sourceCall, scopeID, pi string, callErr error, statusCode int) error {
	if l == nil {
		return nil
	}
	status := ldv.StatusFromHTTP(statusCode)
	if callErr != nil {
		status = ldv.StatusError
	}
	source, _, _ := strings.Cut(scopeID, ":")
	record := ldv.Record{
		TraceID:   call.traceID,
		SpanID:    call.spanID,
		Name:      "dataverwerking.inkomensgegevens-opvragen",
		Status:    status,
		StartTime: call.start,
		EndTime:   time.Now().UTC(),
		Attributes: ldv.Attributes(queryActivity, pi, ldv.SubjectTypePI, "", map[string]any{
			// Where this processing continues: the source logs its half of
			// the request under the same trace id.
			ldv.AttrNextLogbookID: l.nextLogbooks[source],
		}),
	}
	if err := l.Write(ctx, record); err != nil {
		ldv.LogFailure(record.Name, err)
		return err
	}
	return nil
}
