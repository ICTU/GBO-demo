package uses

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeLogbooks struct {
	records map[string][]Record // by trace ID
	down    map[string]bool     // by logbook ID
	slow    map[string]bool
}

func (f fakeLogbooks) Read(ctx context.Context, logbookID, traceID string) ([]Record, error) {
	if f.slow[logbookID] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.down[logbookID] {
		return nil, errors.New("connection refused")
	}
	if logbookID == "99999999900000000200/bd" {
		return nil, ErrNoReadService
	}
	return f.records[traceID], nil
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
