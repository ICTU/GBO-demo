// LDV wiring specific to this source service: working out which
// Dataverwerkingen a GraphQL request performed, and withholding the response
// until they are logged.
//
// The generic client lives in ldv.go; this file is what the bron itself knows.
package main

import (
	"bytes"
	"context"
	"fmt"
	ldv "gbo-demo/ldv-client"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// queryFacts is what the resolvers observed while the query ran: who it was
// about and which belastingjaren it touched. LDV records are constructed from
// the processing, not from the request, so they are collected where the
// processing actually happens rather than guessed from the query text.
type queryFacts struct {
	mutex sync.Mutex
	bsn   string
	years map[int]bool
}

type queryFactsKey struct{}

// withQueryFacts attaches a fresh collector to the request context.
func withQueryFacts(ctx context.Context) (context.Context, *queryFacts) {
	facts := &queryFacts{years: map[int]bool{}}
	return context.WithValue(ctx, queryFactsKey{}, facts), facts
}

func queryFactsFrom(ctx context.Context) *queryFacts {
	facts, _ := ctx.Value(queryFactsKey{}).(*queryFacts)
	return facts
}

// noteSubject records the Betrokkene the query resolved. Nil-safe: the
// resolvers call it unconditionally, and a server without LDV configured
// simply has no collector in the context.
func (f *queryFacts) noteSubject(bsn string) {
	if f == nil || bsn == "" {
		return
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.bsn = bsn
}

func (f *queryFacts) noteYears(years []int) {
	if f == nil {
		return
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	for _, year := range years {
		f.years[year] = true
	}
}

// subject returns the BSN the query resolved, if any.
func (f *queryFacts) subject() string {
	if f == nil {
		return ""
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.bsn
}

// sortedYears returns the belastingjaren the query touched, in order, so the
// records of one request come out in a stable sequence.
func (f *queryFacts) sortedYears() []int {
	if f == nil {
		return nil
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	years := make([]int, 0, len(f.years))
	for year := range f.years {
		years = append(years, year)
	}
	sort.Ints(years)
	return years
}

// sourceLogbook is this bron's view of its Verantwoordelijke's logbook: the
// shared client, plus what only this service knows — which of its
// verwerkingsactiviteiten a given query performed, and who it was about.
//
// A nil *sourceLogbook means the bron is not part of an LDV chain, so the
// methods below are nil-safe and callers need no branch.
type sourceLogbook struct {
	*ldv.Client
	cfg ldvQueryConfig
}

// newSourceLogbook wraps a client, or returns nil when there is none.
func newSourceLogbook(client *ldv.Client, cfg ldvQueryConfig) *sourceLogbook {
	if client == nil {
		return nil
	}
	return &sourceLogbook{Client: client, cfg: cfg}
}

// ldvQueryConfig is the part of the LDV setup that is about this bron rather
// than about the logbook protocol.
type ldvQueryConfig struct {
	// YearActivityTemplate turns a belastingjaar into a register reference,
	// e.g. "bd-ib-%d@v1". Empty means this bron does not describe its
	// verwerkingsactiviteiten per year, and the scope or the fallback decides.
	YearActivityTemplate string
}

// activityForYear names the verwerkingsactiviteit of one processing.
//
// The belastingjaar decides, because a record and its activity have to agree:
// a query covering 2024 and 2025 is two verstrekkingen, and the scope header
// can only ever describe one of them. Preferring the scope made the 2024
// record claim bd-ib-2025@v1 while its own gbo.belastingjaar said 2024 —
// internally contradictory, and wrong in the direction that matters, since
// the register entry is what someone would be held to afterwards.
//
// The scope is the fallback for a request that names no year at all. It maps
// straight onto a reference because the register entries were generated from
// those same scope definitions; both routes land on the same entry for the
// single-year case, which is why the collision went unnoticed until a
// two-year query ran against the real stack.
//
// A reference this bron's register does not hold is refused by the logbook and
// fails the request. That is intended rather than a gap: a verstrekking whose
// verwerkingsactiviteit is not described is one nobody can account for
// afterwards.
func (l *sourceLogbook) activityForYear(scope string, year int) string {
	if l.cfg.YearActivityTemplate != "" && year != 0 {
		return fmt.Sprintf(l.cfg.YearActivityTemplate, year)
	}
	if scope != "" {
		return strings.ReplaceAll(scope, ":", "-") + "@v1"
	}
	return ""
}

// bufferedResponse holds the GraphQL answer until the records are confirmed.
// Without it the data would already be on the wire by the time the logbook
// refused, which is precisely the outcome a fail-closed policy is meant to
// prevent.
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

// flushTo copies the buffered answer to the real response writer.
func (b *bufferedResponse) flushTo(w http.ResponseWriter) {
	for key, values := range b.header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(b.status)
	_, _ = w.Write(b.body.Bytes())
}

// logQuery writes one record per Dataverwerking the query performed: one per
// Betrokkene per verwerkingsactiviteit. A multi-year query is several
// activities and therefore several records, siblings under the sidecar's
// forward span.
//
// A query that resolved no Betrokkene — an introspection query, or a BSN the
// bron does not know — performed no processing of personal data and produces
// no record.
func (l *sourceLogbook) logQuery(ctx context.Context, r *http.Request, facts *queryFacts, start time.Time, status int) error {
	if l == nil {
		return nil
	}
	bsn := facts.subject()
	if bsn == "" {
		return nil
	}
	subjectID, subjectType := l.Subject(r.Header, bsn)
	scope := r.Header.Get("X-GBO-Scope")
	processor := ldv.ForeignProcessor(r)
	traceID := ldv.TraceID(ctx, r.Header)
	parentSpanID := strings.TrimSpace(r.Header.Get(ldv.HeaderParentSpanID))
	end := time.Now().UTC()

	years := facts.sortedYears()
	if len(years) == 0 {
		years = []int{0}
	}
	for _, year := range years {
		attributes := map[string]any{"gbo.scope": scope}
		if year != 0 {
			attributes["gbo.belastingjaar"] = year
		}
		record := ldv.Record{
			TraceID:      traceID,
			SpanID:       ldv.SpanID(),
			ParentSpanID: parentSpanID,
			Name:         "dataverwerking.bronbevraging",
			Status:       ldv.StatusFromHTTP(status),
			StartTime:    start,
			EndTime:      end,
			Attributes: ldv.Attributes(
				l.activityForYear(scope, year),
				subjectID, subjectType, processor, attributes,
			),
		}
		if err := l.Write(ctx, record); err != nil {
			ldv.LogFailure(record.Name, err)
			return err
		}
	}
	return nil
}
