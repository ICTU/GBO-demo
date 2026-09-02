// LDV wiring specific to this source service: working out which
// Dataverwerkingen a GraphQL request performed, about whom, and withholding
// the response until they are logged.
//
// The generic client lives in ldv.go; this file is what the BRP bron itself
// knows. Its distinguishing feature is that one request is about more than one
// Betrokkene: an akte van overlijden names the surviving partner who asked for
// it and the relatives of the deceased. Each of them gets a record.
package main

import (
	"bytes"
	"context"
	ldv "gbo-demo/ldv-client"
	"net/http"
	"sort"
	"sync"
	"time"
)

// The verwerkingsactiviteiten of this bron, as named in RvIG's register.
// Configuration would be over-engineering here: unlike the sidecar, this
// service is not a generic image running in front of an arbitrary bron — it
// *is* the BRP bron, and its processings are the ones its own schema offers.
const (
	akteActivity             = "brp-akte-overlijden@v1"
	persoonsgegevensActivity = "brp-persoonsgegevens-verstrekking@v1"

	// RvIG's own record identifier for a person in the BRP. Not a pseudonym,
	// and it does not need to be: it names someone who appears in a
	// certificate about another person, for whom the source holds no
	// pseudonym at all, and the record never leaves RvIG's own logbook.
	ldvSubjectTypeBRPPersoonID = "brp-persoon-id"
)

// sourceLogbook is this bron's view of RvIG's logbook: the shared client, plus
// what only this service knows — which of its verwerkingsactiviteiten a given
// query performed, and which Betrokkenen it touched.
//
// A nil *sourceLogbook means the bron is not part of an LDV chain, so the
// methods below are nil-safe and callers need no branch.
type sourceLogbook struct {
	*ldv.Client
}

// newSourceLogbook wraps a client, or returns nil when there is none.
func newSourceLogbook(client *ldv.Client) *sourceLogbook {
	if client == nil {
		return nil
	}
	return &sourceLogbook{Client: client}
}

// betrokkene is one data subject of a processing, named the way this
// Verantwoordelijke can name it.
type betrokkene struct {
	id     string
	idType string
	// rol is narrative only, so a reader of the logboek can tell why someone
	// who never asked for anything appears in a record.
	rol string
}

// queryFacts is what the resolvers observed while the query ran. LDV records
// are constructed from the processing rather than from the request, so they
// are collected where the processing actually happens.
type queryFacts struct {
	mutex      sync.Mutex
	bsn        string
	activities map[string]bool
	relatives  []betrokkene
}

type queryFactsKey struct{}

func withQueryFacts(ctx context.Context) (context.Context, *queryFacts) {
	facts := &queryFacts{activities: map[string]bool{}}
	return context.WithValue(ctx, queryFactsKey{}, facts), facts
}

func queryFactsFrom(ctx context.Context) *queryFacts {
	facts, _ := ctx.Value(queryFactsKey{}).(*queryFacts)
	return facts
}

// noteSubject records the Betrokkene the request was about. Nil-safe: the
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

func (f *queryFacts) noteActivity(activity string) {
	if f == nil || activity == "" {
		return
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.activities[activity] = true
}

// noteRelatives records the further Betrokkenen a single processing touched.
func (f *queryFacts) noteRelatives(relatives []betrokkene) {
	if f == nil || len(relatives) == 0 {
		return
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.relatives = append(f.relatives, relatives...)
}

func (f *queryFacts) subject() string {
	if f == nil {
		return ""
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.bsn
}

func (f *queryFacts) sortedActivities() []string {
	if f == nil {
		return nil
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	activities := make([]string, 0, len(f.activities))
	for activity := range f.activities {
		activities = append(activities, activity)
	}
	sort.Strings(activities)
	return activities
}

// otherBetrokkenen returns the further data subjects, de-duplicated and in a
// stable order so the records of one request come out the same way twice.
func (f *queryFacts) otherBetrokkenen() []betrokkene {
	if f == nil {
		return nil
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	seen := map[string]bool{}
	unique := make([]betrokkene, 0, len(f.relatives))
	for _, relative := range f.relatives {
		if relative.id == "" || seen[relative.id] {
			continue
		}
		seen[relative.id] = true
		unique = append(unique, relative)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i].id < unique[j].id })
	return unique
}

// levendeBetrokkenenInAkte returns the further Betrokkenen an akte van
// overlijden processes: the still-living relatives of the deceased who are
// named in it.
//
// The deceased partner is not among them, and that is a judgement rather than
// an oversight: the AVG protects living persons, so the person the certificate
// is about is not a Betrokkene of this processing even though their data is
// what gets disclosed. The record about the surviving partner carries
// `gbo.akte.overledene_verwerkt` so this choice is visible in the logboek
// instead of looking like a component that forgot someone.
//
// The relatives are named by RvIG's own person id. That is not a pseudonym,
// and it does not need to be: the source has no pseudonym for a person who
// never asked for anything, and the record stays inside RvIG's own logbook.
func levendeBetrokkenenInAkte(overledene NatuurlijkPersoon) []betrokkene {
	relatives := make([]betrokkene, 0, len(overledene.HeeftOuder))
	for _, ouder := range overledene.HeeftOuder {
		if stringValue(ouder.DatumOverlijden) != "" || ouder.ID == "" {
			continue
		}
		relatives = append(relatives, betrokkene{
			id:     ouder.ID,
			idType: ldvSubjectTypeBRPPersoonID,
			rol:    "ouder-van-overledene",
		})
	}
	return relatives
}

// bufferedResponse holds the GraphQL answer until the records are confirmed.
// Without it the data would already be on the wire by the time the logbook
// refused, which is precisely what a fail-closed policy is meant to prevent.
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

// logQuery writes the records of one request: one for the Betrokkene the
// request was about, and a child record for every further Betrokkene the same
// processing touched. The child records hang under the first, because they
// exist only as part of that one processing — an akte van overlijden is not
// three independent verstrekkingen.
//
// A query that resolved no Betrokkene — an introspection query, or a BSN the
// bron does not serve — processed no personal data and produces no record.
func (l *sourceLogbook) logQuery(ctx context.Context, r *http.Request, facts *queryFacts, start time.Time, status int) error {
	if l == nil {
		return nil
	}
	bsn := facts.subject()
	if bsn == "" {
		return nil
	}
	subjectID, subjectType := l.Subject(r.Header, bsn)
	processor := ldv.ForeignProcessor(r)
	traceID := ldv.TraceID(ctx, r.Header)
	parentSpanID := ldv.ParentSpanFromHeader(r.Header)
	scope := r.Header.Get("X-GBO-Scope")
	end := time.Now().UTC()

	relatives := facts.otherBetrokkenen()

	for _, activity := range facts.sortedActivities() {
		primarySpanID := ldv.SpanID()
		primary := ldv.Record{
			TraceID:      traceID,
			SpanID:       primarySpanID,
			ParentSpanID: parentSpanID,
			Name:         "dataverwerking.bronbevraging",
			Status:       ldv.StatusFromHTTP(status),
			StartTime:    start,
			EndTime:      end,
			Attributes: ldv.Attributes(activity, subjectID, subjectType, processor, map[string]any{
				"gbo.scope":          scope,
				"gbo.betrokkene.rol": "aanvrager",
				// Named explicitly because the certificate discloses the
				// deceased's data while the deceased is not a Betrokkene: a
				// reader should see that this was decided, not forgotten.
				"gbo.akte.overledene_verwerkt": activity == akteActivity,
			}),
		}
		if err := l.Write(ctx, primary); err != nil {
			ldv.LogFailure(primary.Name, err)
			return err
		}
		for _, relative := range relatives {
			child := ldv.Record{
				TraceID:      traceID,
				SpanID:       ldv.SpanID(),
				ParentSpanID: primarySpanID,
				Name:         "dataverwerking.bronbevraging.medebetrokkene",
				Status:       ldv.StatusFromHTTP(status),
				StartTime:    start,
				EndTime:      end,
				Attributes: ldv.Attributes(activity, relative.id, relative.idType, processor, map[string]any{
					"gbo.scope":          scope,
					"gbo.betrokkene.rol": relative.rol,
				}),
			}
			if err := l.Write(ctx, child); err != nil {
				ldv.LogFailure(child.Name, err)
				return err
			}
		}
	}
	return nil
}
