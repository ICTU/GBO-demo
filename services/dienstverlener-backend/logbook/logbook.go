// Package logbook is the consumer's own Logboek Dataverwerkingen. It is a
// driven adapter for consumer.Logbook.
//
// A private party is not bound by LDV. The demo applies it provisionally, in a
// logbook that is the consumer's own: the read extension starts a chain at the
// application that started the processing, and without the consumer's records
// a reader cannot know which sources one processing touched.
package logbook

import (
	"context"
	"strings"

	"gbo-demo/dienstverlener-backend/consumer"
	ldv "gbo-demo/ldv-client"
)

// Logbook writes through the shared LDV client.
type Logbook struct {
	Client *ldv.Client
	// NextLogbooks maps a source, named by the prefix of its scopes ("bd" in
	// "bd:ib:2025"), onto the read API of that source's logbook — the value of
	// dpl.read.nextLogbookId. In a deployment it comes from the source's
	// service description.
	NextLogbooks map[string]string
}

// ParseNextLogbooks reads a "source=logbookID,source=logbookID" mapping. A
// malformed entry is skipped rather than fatal: a missing pointer costs a
// reader one manual hop, while refusing to start costs every query.
func ParseNextLogbooks(raw string) map[string]string {
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

// Begin mints the identity of one call's record. The trace is the request's
// own: the driving adapter tied it to the transaction id unless a caller
// brought a traceparent. The record has no parent, because the consumer starts
// the chain.
func (l *Logbook) Begin(ctx context.Context) consumer.Position {
	return consumer.Position{TraceID: ldv.TraceID(ctx, nil), SpanID: ldv.SpanID()}
}

// Record writes the call. It returns only once the record is durable in the
// client's spool, so an error withholds the answer.
func (l *Logbook) Record(ctx context.Context, p consumer.Processing) error {
	status := ldv.StatusOK
	if p.Failed {
		status = ldv.StatusError
	}
	source, _, _ := strings.Cut(p.Scope, ":")
	record := ldv.Record{
		TraceID:   p.At.TraceID,
		SpanID:    p.At.SpanID,
		Name:      p.Name,
		Status:    status,
		StartTime: p.Start,
		EndTime:   p.End,
		Attributes: ldv.Attributes(p.Activity, p.Subject, ldv.SubjectTypePI, "", map[string]any{
			// Where this processing continues: the source logs its half of
			// the request under the same trace id.
			ldv.AttrNextLogbookID: l.NextLogbooks[source],
		}),
	}
	if err := l.Client.Write(ctx, record); err != nil {
		ldv.LogFailure(record.Name, err)
		return err
	}
	return nil
}
