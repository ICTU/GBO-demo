package sessionstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"gbo-demo/dienstverlener-backend/consentrequest"
)

func TestASessionExpiresAfterItsLifetime(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m := &Memory{Lifetime: time.Hour, Now: func() time.Time { return now }}
	ctx := context.Background()
	id, err := m.New(ctx, consentrequest.Session{Consent: &consentrequest.Consent{Token: "a.b.c"}})
	if err != nil || len(id) != 43 {
		t.Fatalf("new: %q, %v", id, err)
	}
	if s, err := m.Get(ctx, id); err != nil || s.Consent.Token != "a.b.c" {
		t.Fatalf("get: %+v, %v", s, err)
	}
	now = now.Add(time.Hour)
	if _, err := m.Get(ctx, id); !errors.Is(err, consentrequest.ErrUnknownSession) {
		t.Errorf("get after its lifetime: %v", err)
	}
	if err := m.Update(ctx, id, func(*consentrequest.Session) error { return nil }); !errors.Is(err, consentrequest.ErrUnknownSession) {
		t.Errorf("update after its lifetime: %v", err)
	}
}

func TestAFailedUpdateChangesNothing(t *testing.T) {
	m := &Memory{Lifetime: time.Hour}
	ctx := context.Background()
	id, _ := m.New(ctx, consentrequest.Session{Pending: &consentrequest.Pending{State: "s"}})
	refused := errors.New("refused")
	err := m.Update(ctx, id, func(s *consentrequest.Session) error {
		s.Pending = nil
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("update: %v", err)
	}
	if s, _ := m.Get(ctx, id); s.Pending == nil {
		t.Error("a failed update removed the pending request")
	}
}

func TestSessionIDsDiffer(t *testing.T) {
	m := &Memory{Lifetime: time.Hour}
	a, _ := m.New(context.Background(), consentrequest.Session{})
	b, _ := m.New(context.Background(), consentrequest.Session{})
	if a == b {
		t.Error("two sessions share an id")
	}
}
