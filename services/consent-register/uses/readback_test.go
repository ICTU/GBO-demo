package uses

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeLogbooks struct {
	records map[string][]Record // by trace ID
	down    map[string]bool     // by logbook ID
	slow    map[string]bool
	// truncated marks a trace whose answer the logbook cut off.
	truncated map[string]bool
}

func (f fakeLogbooks) Read(ctx context.Context, logbookID, traceID string) ([]Record, bool, error) {
	if f.slow[logbookID] {
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	if f.down[logbookID] {
		return nil, false, errors.New("connection refused")
	}
	if logbookID == "99999999900000000200/bd" {
		return nil, false, ErrNoReadService
	}
	return f.records[traceID], f.truncated[traceID], nil
}

const (
	bdComponent  = "99999999900000000200/ldv-lezen-bd"
	lvgComponent = "99999999900000000200/ldv-lezen-lvg"
)

var readSources = map[string]string{
	bdComponent:               "Belastingdienst",
	lvgComponent:              "Landelijke Voorziening Gebouwen",
	"99999999900000000200/bd": "Belastingdienst",
}

func TestAUseIsARecordReadBackAtItsStartTime(t *testing.T) {
	registered := time.Date(2026, 11, 19, 9, 0, 1, 0, time.UTC)
	started := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	service := Service{
		Sources: readSources,
		Index: fakeIndex{registrations: map[string][]Registration{"c-1": {
			{LogbookID: bdComponent, TraceID: "t1", RegisteredAt: registered},
		}}},
		Logbooks: fakeLogbooks{records: map[string][]Record{"t1": {{StartTime: started}}}},
	}
	report := service.For(context.Background(), "c-1", "x")
	if len(report.Uses) != 1 || !report.Uses[0].UsedAt.Equal(started) || len(report.Notices) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.SourcesRead) != 1 || report.SourcesRead[0] != "Belastingdienst" {
		t.Errorf("sources read = %v", report.SourcesRead)
	}
}

// #687: a registration without a record gives no use.
func TestARegistrationWithoutARecordIsNoUse(t *testing.T) {
	service := Service{
		Index: fakeIndex{registrations: map[string][]Registration{"c-1": {
			{LogbookID: bdComponent, TraceID: "t-without-record", RegisteredAt: time.Now()},
		}}},
		Logbooks: fakeLogbooks{},
	}
	report := service.For(context.Background(), "c-1", "x")
	if len(report.Uses) != 0 || len(report.Notices) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

// Working answer: several records in one trace are one use each.
func TestEachRecordInATraceIsAUse(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	service := Service{
		Index: fakeIndex{registrations: map[string][]Registration{"c-1": {
			{LogbookID: bdComponent, TraceID: "t1", RegisteredAt: at},
		}}},
		Logbooks: fakeLogbooks{records: map[string][]Record{"t1": {{StartTime: at}, {StartTime: at.Add(time.Second)}}}},
	}
	if got := len(service.For(context.Background(), "c-1", "x").Uses); got != 2 {
		t.Fatalf("uses = %d, want 2", got)
	}
}

// D2: a source that cannot be read gives a notice for that source, and the
// uses of the other sources are still shown.
func TestAnUnreachableSourceGivesItsNoticeAndTheRestStays(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	service := Service{
		Sources: readSources,
		Index: fakeIndex{registrations: map[string][]Registration{"c-1": {
			{LogbookID: bdComponent, TraceID: "t1", RegisteredAt: at},
			{LogbookID: bdComponent, TraceID: "t2", RegisteredAt: at},
			{LogbookID: lvgComponent, TraceID: "t3", RegisteredAt: at},
		}}},
		Logbooks: fakeLogbooks{
			records: map[string][]Record{"t3": {{StartTime: at}}},
			down:    map[string]bool{bdComponent: true},
		},
	}
	report := service.For(context.Background(), "c-1", "x")
	if len(report.Notices) != 1 || report.Notices[0] != (Notice{Kind: NoticeSourceUnavailable, Source: "Belastingdienst"}) {
		t.Fatalf("notices = %+v", report.Notices)
	}
	if len(report.Uses) != 1 || report.Uses[0].Source != "Landelijke Voorziening Gebouwen" {
		t.Fatalf("uses = %+v", report.Uses)
	}
}

func TestASlowSourceIsCutOffByTheTimeout(t *testing.T) {
	service := Service{
		Index: fakeIndex{registrations: map[string][]Registration{"c-1": {
			{LogbookID: bdComponent, TraceID: "t1", RegisteredAt: time.Now()},
		}}},
		Logbooks:    fakeLogbooks{slow: map[string]bool{bdComponent: true}},
		ReadTimeout: 50 * time.Millisecond,
	}
	start := time.Now()
	report := service.For(context.Background(), "c-1", "x")
	if time.Since(start) > time.Second || len(report.Notices) != 1 {
		t.Fatalf("took %v, report %+v", time.Since(start), report)
	}
}

// A source with only the sidecar: its registration is a use without
// read-back, at the time of registration.
func TestALogbookWithoutAReadServiceIsAUseAtRegistration(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	service := Service{
		Index: fakeIndex{registrations: map[string][]Registration{"c-1": {
			{LogbookID: "99999999900000000200/bd", TraceID: "t1", RegisteredAt: at},
		}}},
		Logbooks: fakeLogbooks{},
	}
	report := service.For(context.Background(), "c-1", "x")
	if len(report.Uses) != 1 || !report.Uses[0].UsedAt.Equal(at) {
		t.Fatalf("report = %+v", report)
	}
}

type countingLogbooks struct {
	mu       sync.Mutex
	running  int
	maxSeen  int
	duration time.Duration
}

func (c *countingLogbooks) Read(ctx context.Context, _, _ string) ([]Record, bool, error) {
	c.mu.Lock()
	c.running++
	if c.running > c.maxSeen {
		c.maxSeen = c.running
	}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.running--; c.mu.Unlock() }()
	select {
	case <-time.After(c.duration):
		return []Record{{StartTime: time.Now()}}, false, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

// A consent with many uses opens at most ReadsPerSource reads at one
// source at once, and still shows every use.
func TestManyUsesAreReadAFewAtATime(t *testing.T) {
	var registrations []Registration
	for i := 0; i < 60; i++ {
		registrations = append(registrations, Registration{LogbookID: bdComponent, TraceID: string(rune('a' + i)), RegisteredAt: time.Now()})
	}
	logbooks := &countingLogbooks{duration: 5 * time.Millisecond}
	service := Service{
		Index:          fakeIndex{registrations: map[string][]Registration{"c-1": registrations}},
		Logbooks:       logbooks,
		ReadsPerSource: 4,
	}
	report := service.For(context.Background(), "c-1", "x")
	if len(report.Uses) != 60 || len(report.Notices) != 0 {
		t.Fatalf("uses %d, notices %+v", len(report.Uses), report.Notices)
	}
	if logbooks.maxSeen > 4 {
		t.Fatalf("%d reads ran at once at one source, want at most 4", logbooks.maxSeen)
	}
}

// A source that cut its answer off gets a notice, next to the uses it did
// return: the list is not presented as the whole history.
func TestATruncatedSourceGetsANotice(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	service := Service{
		Sources: readSources,
		Index: fakeIndex{registrations: map[string][]Registration{"c-1": {
			{LogbookID: bdComponent, TraceID: "t1", RegisteredAt: at},
		}}},
		Logbooks: fakeLogbooks{
			records:   map[string][]Record{"t1": {{StartTime: at}, {StartTime: at}}},
			truncated: map[string]bool{"t1": true},
		},
	}
	report := service.For(context.Background(), "c-1", "x")
	if len(report.Uses) != 2 {
		t.Fatalf("uses = %d, want the 2 returned", len(report.Uses))
	}
	if len(report.Notices) != 1 || report.Notices[0] != (Notice{Kind: NoticeSourceIncomplete, Source: "Belastingdienst"}) {
		t.Fatalf("notices = %+v", report.Notices)
	}
}
