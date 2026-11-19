// Package uses answers when a consent was used: the core of
// GET /consents/{id}/uses (#687).
//
// A use is a delivery a source registered at the consent usage index under
// this consent. On this path a registration is the use: the index holds one
// per trace per source, and the register shows each as the moment the data
// went out (the plan's short path; the long path reads each one back from
// the source's logbook first).
//
// The package imports no transport. The index is a port; the adapter that
// reaches it over HTTP is usageindex.
package uses

import (
	"context"
	"sort"
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

// Use is one moment the consent was used, as the citizen sees it.
type Use struct {
	// UsedAt is the time of registration: the delivery and its registration
	// happen in the same request, milliseconds apart.
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
	uses := make([]Use, 0, len(registrations))
	for _, registration := range registrations {
		uses = append(uses, Use{
			UsedAt:             registration.RegisteredAt,
			ServiceProviderOIN: serviceProviderOIN,
			ServiceProvider:    provider,
			Source:             s.sourceName(registration.LogbookID),
			LogbookID:          registration.LogbookID,
		})
	}
	sort.SliceStable(uses, func(a, b int) bool { return uses[a].UsedAt.After(uses[b].UsedAt) })
	return Report{Uses: uses, Notices: []Notice{}}
}

func (s Service) sourceName(logbookID string) string {
	if name := s.Sources[logbookID]; name != "" {
		return name
	}
	return logbookID
}
