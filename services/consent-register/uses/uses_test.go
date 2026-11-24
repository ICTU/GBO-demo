package uses

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeIndex struct {
	registrations map[string][]Registration
	err           error
}

func (f fakeIndex) Registrations(_ context.Context, consentID string) ([]Registration, error) {
	return f.registrations[consentID], f.err
}

func TestOneUsePerRegistrationNewestFirst(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	service := Service{
		Sources:          readSources,
		ServiceProviders: map[string]string{"99999999900000000300": "Hypotheek-BV"},
		Index: fakeIndex{registrations: map[string][]Registration{
			"c-1": {
				{LogbookID: bdComponent, TraceID: "a", RegisteredAt: at},
				{LogbookID: lvgComponent, TraceID: "b", RegisteredAt: at.Add(time.Minute)},
			},
		}},
		Logbooks: fakeLogbooks{records: map[string][]Record{"a": {{StartTime: at}}, "b": {{StartTime: at.Add(time.Minute)}}}},
	}

	report := service.For(context.Background(), "c-1", "99999999900000000300")
	if len(report.Notices) != 0 {
		t.Fatalf("notices = %v", report.Notices)
	}
	if len(report.Uses) != 2 {
		t.Fatalf("uses = %v", report.Uses)
	}
	first := report.Uses[0]
	if first.Source != "Landelijke Voorziening Gebouwen" || !first.UsedAt.Equal(at.Add(time.Minute)) ||
		first.ServiceProviderOIN != "99999999900000000300" || first.ServiceProvider != "Hypotheek-BV" {
		t.Errorf("first use = %+v", first)
	}
}

func TestNoRegistrationsIsAnEmptyListWithoutANotice(t *testing.T) {
	report := Service{Index: fakeIndex{}}.For(context.Background(), "c-1", "x")
	if report.Uses == nil || len(report.Uses) != 0 || len(report.Notices) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestAnIndexThatDoesNotAnswerGivesTheNoticeNotAnEmptyList(t *testing.T) {
	for name, service := range map[string]Service{
		"down":           {Index: fakeIndex{err: errors.New("connection refused")}},
		"not configured": {},
	} {
		t.Run(name, func(t *testing.T) {
			report := service.For(context.Background(), "c-1", "x")
			if len(report.Notices) != 1 || report.Notices[0].Kind != NoticeIndexUnavailable {
				t.Fatalf("notices = %v", report.Notices)
			}
		})
	}
}

func TestAnUnknownLogbookIsShownAsItself(t *testing.T) {
	service := Service{
		Index: fakeIndex{registrations: map[string][]Registration{
			"c-1": {{LogbookID: "99999999900000000900/x", TraceID: "a", RegisteredAt: time.Now()}},
		}},
		Logbooks: fakeLogbooks{records: map[string][]Record{"a": {{StartTime: time.Now()}}}},
	}
	if got := service.For(context.Background(), "c-1", "x").Uses[0].Source; got != "99999999900000000900/x" {
		t.Fatalf("source = %q", got)
	}
}
