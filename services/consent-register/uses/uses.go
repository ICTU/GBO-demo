// Package uses answers when a consent was used: the core of
// GET /consents/{id}/uses (#687).
//
// A use is a delivery a source registered at the consent usage index under
// this consent, read back from that source's logbook (#687): the register
// looks up the registrations, reads each trace at the source's LDV
// component over FSC, and shows one use per record it finds there, at the
// record's start time. A registration without a record gives no use.
//
// A registration whose logbook has no read service (a source that runs only
// the sidecar) is shown as a use without read-back, at the time of
// registration: hiding it would hide a real delivery.
//
// The package imports no transport. The index is a port; the adapter that
// reaches it over HTTP is usageindex.
package uses

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// Registration is one pointer the index holds for a consent.
type Registration struct {
	LogbookID    string
	TraceID      string
	RegisteredAt time.Time
}

// Index is the consent usage index seen from the register. An error means
// the index could not answer, not that there are no registrations.
type Index interface {
	Registrations(ctx context.Context, consentID string) ([]Registration, error)
}

// Record is one record a source's logbook holds under a trace, as far as
// the register needs it.
type Record struct {
	StartTime time.Time
}

// Logbooks reads the logbook of a source's LDV component by trace-id, over
// FSC. ErrNoReadService means the register has no read service for that
// logbook ID; any other error means the logbook could not be read.
type Logbooks interface {
	Read(ctx context.Context, logbookID, traceID string) ([]Record, error)
}

// ErrNoReadService is returned for a logbook the register cannot read.
var ErrNoReadService = errors.New("no read service for this logbook")

// DefaultReadTimeout bounds all reads of one source's logbook together.
const DefaultReadTimeout = 5 * time.Second

// DefaultReadsPerSource is how many reads of one source run at once.
const DefaultReadsPerSource = 8

// Use is one moment the consent was used, as the citizen sees it.
type Use struct {
	// UsedAt is the start time of the record read back from the source
	// (#687), or the time of registration for a logbook without a read
	// service.
	UsedAt time.Time
	// ServiceProviderOIN is the party the consent was given to. A
	// registration does not name it; the consent does.
	ServiceProviderOIN string
	// ServiceProvider is its name, ready to show (#687: the frontend does
	// no derivation). The OIN when the name is not configured.
	ServiceProvider string
	// Source is the name of the source that delivered, from the logbook ID.
	Source    string
	LogbookID string
}

// NoticeIndexUnavailable says the index could not be reached, so the list of
// uses is not known (decision D2): "none" and "not available" stay apart.
const NoticeIndexUnavailable = "index_unavailable"

// NoticeSourceUnavailable says one source's logbook could not be read, so
// its uses are missing from the list (D2); the uses of other sources are
// still shown.
const NoticeSourceUnavailable = "source_unavailable"

// Notice is something the citizen must be told instead of, or next to, the
// uses.
type Notice struct {
	Kind string
	// Source names the source a notice is about; empty for the index.
	Source string
}

// Report is the answer for one consent.
type Report struct {
	Uses    []Use
	Notices []Notice
	// IndexErr is why the index did not answer, for the register's own log;
	// the citizen sees the notice.
	IndexErr error
	// SourcesRead names the sources whose logbooks the register read, for
	// the register's own record of that reading.
	SourcesRead []string
	// SourceErrs is why a source's logbook could not be read, by source.
	SourceErrs map[string]error
}

// Service lists the uses of a consent.
type Service struct {
	// Index may be nil: a register without an index cannot know the uses,
	// and says so with the same notice as an index that is down.
	Index Index
	// Sources names the source behind each logbook ID. An unknown logbook
	// ID is shown as itself rather than hidden: it is still a delivery.
	Sources map[string]string
	// ServiceProviders names the party behind each OIN, for the same reason.
	ServiceProviders map[string]string
	// Logbooks reads a source's logbook back. Nil means no read-back: every
	// registration is a use, as on the short path.
	Logbooks Logbooks
	// ReadTimeout bounds all reads of one source together;
	// DefaultReadTimeout when zero.
	ReadTimeout time.Duration
	// ReadsPerSource caps the reads of one source that run at once;
	// DefaultReadsPerSource when zero.
	ReadsPerSource int
}

// For returns the uses of the consent, newest first.
func (s Service) For(ctx context.Context, consentID, serviceProviderOIN string) Report {
	if s.Index == nil {
		return Report{Uses: []Use{}, Notices: []Notice{{Kind: NoticeIndexUnavailable}}}
	}
	registrations, err := s.Index.Registrations(ctx, consentID)
	if err != nil {
		return Report{Uses: []Use{}, Notices: []Notice{{Kind: NoticeIndexUnavailable}}, IndexErr: err}
	}
	provider := s.ServiceProviders[serviceProviderOIN]
	if provider == "" {
		provider = serviceProviderOIN
	}
	use := func(registration Registration, at time.Time) Use {
		return Use{
			UsedAt:             at,
			ServiceProviderOIN: serviceProviderOIN,
			ServiceProvider:    provider,
			Source:             s.sourceName(registration.LogbookID),
			LogbookID:          registration.LogbookID,
		}
	}

	results := s.readBack(ctx, registrations)
	report := Report{Uses: []Use{}, Notices: []Notice{}}
	failed := map[string]bool{}
	read := map[string]bool{}
	for i, registration := range registrations {
		result := results[i]
		source := s.sourceName(registration.LogbookID)
		switch {
		case errors.Is(result.err, ErrNoReadService):
			report.Uses = append(report.Uses, use(registration, registration.RegisteredAt))
		case result.err != nil:
			if !failed[source] {
				failed[source] = true
				report.Notices = append(report.Notices, Notice{Kind: NoticeSourceUnavailable, Source: source})
				if report.SourceErrs == nil {
					report.SourceErrs = map[string]error{}
				}
				report.SourceErrs[source] = result.err
			}
		default:
			if !read[source] {
				read[source] = true
				report.SourcesRead = append(report.SourcesRead, source)
			}
			for _, record := range result.records {
				report.Uses = append(report.Uses, use(registration, record.StartTime))
			}
		}
	}
	sort.SliceStable(report.Uses, func(a, b int) bool { return report.Uses[a].UsedAt.After(report.Uses[b].UsedAt) })
	sort.Strings(report.SourcesRead)
	return report
}

type readResult struct {
	records []Record
	err     error
}

// readBack reads every registration's trace at its source. Sources are read
// in parallel, each within one deadline (ReadTimeout), so one slow source
// cannot hold the page. Within a source at most ReadsPerSource reads run at
// once: the read API takes one traceId per request (#686), and a consent
// with many uses would otherwise open hundreds of FSC requests at the same
// Inway at once, which it refuses (TRANSACTION_LOG_WRITE_ERROR). A read the
// deadline cuts off counts as the source being unavailable.
func (s Service) readBack(ctx context.Context, registrations []Registration) []readResult {
	results := make([]readResult, len(registrations))
	if s.Logbooks == nil {
		for i := range results {
			results[i].err = ErrNoReadService
		}
		return results
	}
	timeout := s.ReadTimeout
	if timeout <= 0 {
		timeout = DefaultReadTimeout
	}
	perSource := s.ReadsPerSource
	if perSource <= 0 {
		perSource = DefaultReadsPerSource
	}
	bySource := map[string][]int{}
	for i, registration := range registrations {
		bySource[registration.LogbookID] = append(bySource[registration.LogbookID], i)
	}
	var wait sync.WaitGroup
	for _, indexes := range bySource {
		wait.Add(1)
		go func() {
			defer wait.Done()
			sourceCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			slots := make(chan struct{}, perSource)
			var reads sync.WaitGroup
			for _, i := range indexes {
				reads.Add(1)
				go func() {
					defer reads.Done()
					select {
					case slots <- struct{}{}:
					case <-sourceCtx.Done():
						results[i] = readResult{err: sourceCtx.Err()}
						return
					}
					defer func() { <-slots }()
					records, err := s.Logbooks.Read(sourceCtx, registrations[i].LogbookID, registrations[i].TraceID)
					results[i] = readResult{records: records, err: err}
				}()
			}
			reads.Wait()
		}()
	}
	wait.Wait()
	return results
}

func (s Service) sourceName(logbookID string) string {
	if name := s.Sources[logbookID]; name != "" {
		return name
	}
	return logbookID
}
