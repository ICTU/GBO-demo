package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"data-usage-index/internal/usage"
)

func registration(trace string, at time.Time) usage.Registration {
	return usage.Registration{
		LogbookID:     "99999999900000000200/bd",
		TraceID:       trace,
		GrondslagType: usage.GrondslagConsent,
		GrondslagRef:  "c-1",
		RegisteredAt:  at,
	}
}

func TestRegistrationsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	ctx := context.Background()
	at := time.Date(2026, 11, 19, 9, 0, 0, 123456789, time.UTC)

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.Add(ctx, registration("4bf92f3577b34da6a3ce929d0e0e4736", at)); err != nil || !created {
		t.Fatalf("add: created=%v err=%v", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.ByGrondslag(ctx, usage.GrondslagConsent, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].RegisteredAt.Equal(at) {
		t.Fatalf("want the one registration at %v after a restart, got %+v", at, got)
	}
}

func TestADuplicateReturnsTheStoredRegistration(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	first := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	if _, _, err := store.Add(ctx, registration("4bf92f3577b34da6a3ce929d0e0e4736", first)); err != nil {
		t.Fatal(err)
	}
	again, created, err := store.Add(ctx, registration("4bf92f3577b34da6a3ce929d0e0e4736", first.Add(time.Hour)))
	if err != nil || created {
		t.Fatalf("duplicate: created=%v err=%v", created, err)
	}
	if !again.RegisteredAt.Equal(first) {
		t.Fatalf("want the first time %v, got %v", first, again.RegisteredAt)
	}
}

func TestByGrondslagIsNewestFirst(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	base := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	// 9 and 10 seconds: a variable-width format would sort these wrongly.
	for i, trace := range []string{"00000000000000000000000000000009", "0000000000000000000000000000000a"} {
		if _, _, err := store.Add(ctx, registration(trace, base.Add(time.Duration(9+i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ByGrondslag(ctx, usage.GrondslagConsent, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TraceID != "0000000000000000000000000000000a" {
		t.Fatalf("want newest first, got %+v", got)
	}
}
