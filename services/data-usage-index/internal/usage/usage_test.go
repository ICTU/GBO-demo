package usage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// memoryStore is the in-memory fake the core is tested against.
type memoryStore struct {
	registrations []Registration
}

func (m *memoryStore) Add(_ context.Context, r Registration) (Registration, bool, error) {
	for _, existing := range m.registrations {
		if existing.LogbookID == r.LogbookID && existing.TraceID == r.TraceID &&
			existing.GrondslagType == r.GrondslagType && existing.GrondslagRef == r.GrondslagRef {
			return existing, false, nil
		}
	}
	m.registrations = append(m.registrations, r)
	return r, true, nil
}

func (m *memoryStore) ByGrondslag(_ context.Context, typ, ref string) ([]Registration, error) {
	var out []Registration
	for _, r := range m.registrations {
		if r.GrondslagType == typ && r.GrondslagRef == ref {
			out = append(out, r)
		}
	}
	return out, nil
}

const (
	bdPeer  = "99999999900000000200"
	brpPeer = "99999999900000000400"
	traceA  = "4bf92f3577b34da6a3ce929d0e0e4736"
	traceB  = "00f067aa0ba902b700f067aa0ba902b7"
)

func request(logbook, trace string) Request {
	return Request{LogbookID: logbook, TraceID: trace, GrondslagType: GrondslagConsent, GrondslagRef: "c-1"}
}

func clock(times ...time.Time) func() time.Time {
	return func() time.Time {
		t := times[0]
		if len(times) > 1 {
			times = times[1:]
		}
		return t
	}
}

func TestRegisterStoresAndARetryFindsTheSameRegistration(t *testing.T) {
	first := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	index := New(&memoryStore{}, clock(first, first.Add(time.Minute)))

	stored, created, err := index.Register(context.Background(), bdPeer, request(bdPeer+"/bd", traceA))
	if err != nil || !created {
		t.Fatalf("first registration: created=%v err=%v", created, err)
	}
	again, created, err := index.Register(context.Background(), bdPeer, request(bdPeer+"/bd", traceA))
	if err != nil || created {
		t.Fatalf("retry: created=%v err=%v", created, err)
	}
	if !again.RegisteredAt.Equal(stored.RegisteredAt) {
		t.Fatalf("a retry must return the stored registration, got %v want %v", again.RegisteredAt, stored.RegisteredAt)
	}
}

func TestRegisterRefusesAnotherPeersLogbook(t *testing.T) {
	index := New(&memoryStore{}, nil)
	_, _, err := index.Register(context.Background(), brpPeer, request(bdPeer+"/bd", traceA))
	if !errors.Is(err, ErrForeignLogbook) {
		t.Fatalf("want ErrForeignLogbook, got %v", err)
	}
}

func TestRegisterRefusesARequestNotOverFSC(t *testing.T) {
	index := New(&memoryStore{}, nil)
	_, _, err := index.Register(context.Background(), "", request(bdPeer+"/bd", traceA))
	if !errors.Is(err, ErrNotOverFSC) {
		t.Fatalf("want ErrNotOverFSC, got %v", err)
	}
}

func TestRegisterValidatesItsFields(t *testing.T) {
	index := New(&memoryStore{}, nil)
	cases := map[string]Request{
		"logbook without name":   request(bdPeer, traceA),
		"logbook with bad peer":  request("2000/bd", traceA),
		"logbook with uppercase": request(bdPeer+"/BD", traceA),
		"uuid trace id":          request(bdPeer+"/bd", "4bf92f35-77b3-4da6-a3ce-929d0e0e4736"),
		"zero trace id":          request(bdPeer+"/bd", "00000000000000000000000000000000"),
		"uppercase trace id":     request(bdPeer+"/bd", "4BF92F3577B34DA6A3CE929D0E0E4736"),
		"other legal basis":      {LogbookID: bdPeer + "/bd", TraceID: traceA, GrondslagType: "wet", GrondslagRef: "c-1"},
		"empty reference":        {LogbookID: bdPeer + "/bd", TraceID: traceA, GrondslagType: GrondslagConsent},
		"reference with space":   {LogbookID: bdPeer + "/bd", TraceID: traceA, GrondslagType: GrondslagConsent, GrondslagRef: "c 1"},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := index.Register(context.Background(), bdPeer, r); !errors.Is(err, ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestLookupReturnsOneConsentsRegistrationsNewestFirst(t *testing.T) {
	start := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	index := New(&memoryStore{}, clock(start, start.Add(time.Minute), start.Add(2*time.Minute)))
	ctx := context.Background()
	if _, _, err := index.Register(ctx, bdPeer, request(bdPeer+"/bd", traceA)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := index.Register(ctx, bdPeer, request(bdPeer+"/lvg", traceB)); err != nil {
		t.Fatal(err)
	}
	other := request(bdPeer+"/bd", traceB)
	other.GrondslagRef = "c-2"
	if _, _, err := index.Register(ctx, bdPeer, other); err != nil {
		t.Fatal(err)
	}

	got, err := index.Lookup(ctx, GrondslagConsent, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].LogbookID != bdPeer+"/lvg" || got[1].LogbookID != bdPeer+"/bd" {
		t.Fatalf("want lvg then bd for c-1, got %+v", got)
	}
}
