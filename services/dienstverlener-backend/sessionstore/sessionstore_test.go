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

func TestDeleteRemovesTheSession(t *testing.T) {
	m := &Memory{Lifetime: time.Hour}
	ctx := context.Background()
	id, _ := m.New(ctx, consentrequest.Session{})
	if err := m.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Get(ctx, id); !errors.Is(err, consentrequest.ErrUnknownSession) {
		t.Errorf("get after delete: %v", err)
	}
	if err := m.Delete(ctx, id); err != nil {
		t.Errorf("delete of an unknown session: %v", err)
	}
}

// New sweeps expired sessions at most once per sweepInterval, not on every
// call.
func TestExpiredSessionsAreSweptAtMostOnceAMinute(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m := &Memory{Lifetime: time.Minute, Now: func() time.Time { return now }}
	ctx := context.Background()
	_, _ = m.New(ctx, consentrequest.Session{}) // sweeps the empty store
	now = now.Add(30 * time.Second)
	_, _ = m.New(ctx, consentrequest.Session{})
	now = now.Add(45 * time.Second) // the first session has expired
	_, _ = m.New(ctx, consentrequest.Session{})
	if got := m.Len(); got != 2 {
		t.Errorf("after 75 s: %d sessions, want 2 (one expired, swept)", got)
	}
	now = now.Add(20 * time.Second) // the second session has expired too
	_, _ = m.New(ctx, consentrequest.Session{})
	if got := m.Len(); got != 3 {
		t.Errorf("20 s after a sweep: %d sessions, want 3 (one expired, not yet swept)", got)
	}
}
