// LDV wiring specific to this source: which Dataverwerking an ownership
// check performed, and withholding the answer until it is recorded.
package main

import (
	"bytes"
	"context"
	ldv "gbo-demo/ldv-client"
	"net/http"
	"strings"
	"sync"
	"time"
)

// queryFacts is what the resolver observed while the query ran: who it was
// about. Records describe the processing, so the subject is collected where
// the lookup happens rather than guessed from the query text.
type queryFacts struct {
	mutex sync.Mutex
	bsn   string
}

type queryFactsKey struct{}

func withQueryFacts(ctx context.Context) (context.Context, *queryFacts) {
	facts := &queryFacts{}
	return context.WithValue(ctx, queryFactsKey{}, facts), facts
}

func queryFactsFrom(ctx context.Context) *queryFacts {
	facts, _ := ctx.Value(queryFactsKey{}).(*queryFacts)
	return facts
}

// noteSubject is nil-safe: a server without LDV has no collector.
func (f *queryFacts) noteSubject(bsn string) {
	if f == nil || bsn == "" {
		return
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.bsn = bsn
}

func (f *queryFacts) subject() string {
	if f == nil {
		return ""
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.bsn
}

// ldvQueryConfig names this source's verwerkingsactiviteiten.
type ldvQueryConfig struct {
	// ScopeActivityBase turns the scope into a register URI:
	// lvg:vbo:eigendom becomes <base>/lvg-vbo-eigendom/v1.
	ScopeActivityBase string
}

// sourceLogbook is nil when the source is not part of an LDV chain; its
// methods are nil-safe so callers need no branch.
type sourceLogbook struct {
	*ldv.Client
	cfg ldvQueryConfig
}

func newSourceLogbook(client *ldv.Client, cfg ldvQueryConfig) *sourceLogbook {
	if client == nil {
		return nil
	}
	return &sourceLogbook{Client: client, cfg: cfg}
}

func (l *sourceLogbook) activity(scope string) string {
	if scope == "" || l.cfg.ScopeActivityBase == "" {
		return ""
	}
	return strings.TrimRight(l.cfg.ScopeActivityBase, "/") + "/" + strings.ReplaceAll(scope, ":", "-") + "/v1"
}

// logQuery writes one record per ownership check. A query that resolved no
// Betrokkene (introspection) processed no personal data and writes nothing.
func (l *sourceLogbook) logQuery(ctx context.Context, r *http.Request, facts *queryFacts, start time.Time, status int) error {
	if l == nil {
		return nil
	}
	bsn := facts.subject()
	if bsn == "" {
		return nil
	}
	subjectID, subjectType, err := l.Subject(r.Header, bsn)
	if err != nil {
		ldv.LogFailure("dataverwerking.bronbevraging", err)
		return err
	}
	traceID := ldv.TraceID(ctx, r.Header)
	record := ldv.Record{
		TraceID:      traceID,
		SpanID:       ldv.SpanID(),
		ParentSpanID: ldv.ParentSpanFor(r.Header, traceID),
		Name:         "dataverwerking.bronbevraging",
		Status:       ldv.StatusFromHTTP(status),
		StartTime:    start,
		EndTime:      time.Now().UTC(),
		Attributes: ldv.Attributes(
			l.activity(r.Header.Get("X-GBO-Scope")),
			subjectID, subjectType, l.ForeignProcessor(r), nil,
		),
	}
	if err := l.Write(ctx, record); err != nil {
		ldv.LogFailure(record.Name, err)
		return err
	}
	return nil
}

// bufferedResponse holds the GraphQL answer until the record is spooled.
type bufferedResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: http.Header{}, status: http.StatusOK}
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) Write(chunk []byte) (int, error) { return b.body.Write(chunk) }

func (b *bufferedResponse) WriteHeader(status int) { b.status = status }

func (b *bufferedResponse) flushTo(w http.ResponseWriter) {
	for key, values := range b.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}
