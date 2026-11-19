package ldv

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeIndex struct {
	registered [][2]string
	failure    error
}

func (f *fakeIndex) Register(_ context.Context, traceID, consentID string) error {
	if f.failure != nil {
		return f.failure
	}
	f.registered = append(f.registered, [2]string{traceID, consentID})
	return nil
}

const deliveryActivity = "https://logboek.belastingdienst.nl/verwerkingsactiviteiten/bd-ib-2025/v1"

func delivery() Delivery {
	start := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	return Delivery{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7",
		Start: start, End: start.Add(40 * time.Millisecond),
		ConsentID: "c-7f3a", SubjectID: "LP-abc123",
	}
}

func recorderUnderTest(t *testing.T, repository *fakeRepository, index *fakeIndex) *DeliveryRecorder {
	t.Helper()
	logbook, err := NewLogbook(repository, testRegister(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := NewDeliveryRecorder(logbook, index, deliveryActivity)
	if err != nil {
		t.Fatal(err)
	}
	return recorder
}

// #689: one record with the request's trace-id, then one registration with
// the trace-id and the consent ID.
func TestADeliveryIsRecordedThenRegistered(t *testing.T) {
	repository, index := &fakeRepository{}, &fakeIndex{}
	if err := recorderUnderTest(t, repository, index).Record(context.Background(), delivery()); err != nil {
		t.Fatal(err)
	}
	if len(repository.stored) != 1 {
		t.Fatalf("records = %d, want 1", len(repository.stored))
	}
	record := repository.stored[0]
	if record.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || record.Status != StatusOK || record.Name != RecordName {
		t.Errorf("record = %+v", record.Record)
	}
	if got := record.ProcessingActivityID(); got != deliveryActivity {
		t.Errorf("activity = %q", got)
	}
	if len(index.registered) != 1 || index.registered[0] != [2]string{"4bf92f3577b34da6a3ce929d0e0e4736", "c-7f3a"} {
		t.Errorf("registrations = %v", index.registered)
	}
}

// #689: the record names the citizen, not the consent.
func TestTheRecordNamesTheCitizenNotTheConsent(t *testing.T) {
	repository := &fakeRepository{}
	if err := recorderUnderTest(t, repository, &fakeIndex{}).Record(context.Background(), delivery()); err != nil {
		t.Fatal(err)
	}
	record := repository.stored[0]
	if record.DataSubjectID() != "LP-abc123" {
		t.Errorf("data subject = %q", record.DataSubjectID())
	}
	for key, value := range record.Attributes {
		if text, _ := value.(string); text == "c-7f3a" {
			t.Errorf("the consent ID is in the record under %s", key)
		}
	}
}

func TestARecordThatCannotBeStoredIsNotRegistered(t *testing.T) {
	index := &fakeIndex{}
	err := recorderUnderTest(t, &fakeRepository{failure: errors.New("disk full")}, index).Record(context.Background(), delivery())
	if !errors.Is(err, ErrDeliveryNotRecorded) {
		t.Fatalf("err = %v", err)
	}
	if len(index.registered) != 0 {
		t.Error("a trace without a record was registered")
	}
}

func TestAFailedRegistrationIsAnError(t *testing.T) {
	err := recorderUnderTest(t, &fakeRepository{}, &fakeIndex{failure: errors.New("index down")}).Record(context.Background(), delivery())
	if !errors.Is(err, ErrDeliveryNotRecorded) {
		t.Fatalf("err = %v", err)
	}
}

func TestADeliveryWithoutASubjectOrConsentIsRefused(t *testing.T) {
	for name, change := range map[string]func(*Delivery){
		"no subject": func(d *Delivery) { d.SubjectID = "" },
		"no consent": func(d *Delivery) { d.ConsentID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			d := delivery()
			change(&d)
			repository := &fakeRepository{}
			if err := recorderUnderTest(t, repository, &fakeIndex{}).Record(context.Background(), d); !errors.Is(err, ErrDeliveryNotRecorded) {
				t.Fatalf("err = %v", err)
			}
			if len(repository.stored) != 0 {
				t.Error("a record was written")
			}
		})
	}
}

func TestAnUnknownActivityIsRefusedAtStart(t *testing.T) {
	logbook, err := NewLogbook(&fakeRepository{}, testRegister(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDeliveryRecorder(logbook, &fakeIndex{}, "https://example.test/unknown/v1"); err == nil {
		t.Fatal("an activity outside the register was accepted")
	}
}
