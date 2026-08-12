package main

import (
	"testing"
	"time"
)

// eudiSpan is an entry-point span for the EUDI adapter, the shape ingest()
// sees when a wallet triggers an issuance for usecase.
func eudiSpan(traceID, usecase string) traceSpan {
	return traceSpan{
		TraceID: traceID,
		Service: "eudi-adapter",
		Attributes: map[string]string{
			"http.method": "POST",
			"http.target": "/" + usecase + "/",
			"gbo.usecase": usecase,
		},
	}
}

// dvtpSpan is an entry-point span for a consumer query — no usecase
// attribute, because only the EUDI flow has one.
func dvtpSpan(traceID string) traceSpan {
	return traceSpan{
		TraceID: traceID,
		Service: "dienstverlener-backend",
		Attributes: map[string]string{
			"http.method": "POST",
			"http.target": "/api/dvtp/query",
		},
	}
}

// taggedBy is what a span looks like once the frontend passed on the
// dev-portal's session cookie and its backend stamped the header on the span.
func taggedBy(s traceSpan, session string) traceSpan {
	s.Attributes["gbo.demo.session"] = session
	return s
}

func recv(t *testing.T, ch chan watchEvent) (watchEvent, bool) {
	t.Helper()
	select {
	case evt := <-ch:
		return evt, true
	case <-time.After(time.Second):
		return watchEvent{}, false
	}
}

func mustNotRecv(t *testing.T, ch chan watchEvent, what string) {
	t.Helper()
	select {
	case evt := <-ch:
		t.Fatalf("%s: unexpected wake-up for trace %s", what, evt.TraceID)
	case <-time.After(50 * time.Millisecond):
	}
}

// Two people each waiting on their own wallet-QR must not pick up each
// other's scan — the whole point of the usecase filter.
func TestWatchNextIgnoresOtherUsecase(t *testing.T) {
	hub := newTraceHub(time.Minute)

	mine, cleanupMine := hub.watchNext("", "inkomensverklaring_2024")
	defer cleanupMine()
	theirs, cleanupTheirs := hub.watchNext("", "akte_van_overlijden")
	defer cleanupTheirs()

	hub.ingest(eudiSpan("aaaa", "akte_van_overlijden"))

	evt, ok := recv(t, theirs)
	if !ok {
		t.Fatal("watcher for akte_van_overlijden was not woken by its own trace")
	}
	if evt.TraceID != "aaaa" {
		t.Errorf("trace_id = %q, want %q", evt.TraceID, "aaaa")
	}
	if evt.Shared {
		t.Error("shared = true, want false: only one watcher matched")
	}
	mustNotRecv(t, mine, "watcher for inkomensverklaring_2024")

	// Still armed: its own trace arrives afterwards and lands.
	hub.ingest(eudiSpan("bbbb", "inkomensverklaring_2024"))
	if evt, ok := recv(t, mine); !ok {
		t.Fatal("watcher was disarmed by someone else's trace")
	} else if evt.TraceID != "bbbb" {
		t.Errorf("trace_id = %q, want %q", evt.TraceID, "bbbb")
	}
}

// A trace that names no usecase (issuance, use, or anything hitting the
// adapter without the attribute) still reaches a filtered watcher. Waiting
// forever is a worse failure than seeing one run too many.
func TestWatchNextDeliversUntaggedTraceToFilteredWatcher(t *testing.T) {
	hub := newTraceHub(time.Minute)

	ch, cleanup := hub.watchNext("", "inkomensverklaring_2024")
	defer cleanup()

	hub.ingest(dvtpSpan("cccc"))

	if evt, ok := recv(t, ch); !ok {
		t.Fatal("filtered watcher missed an untagged trace")
	} else if evt.TraceID != "cccc" {
		t.Errorf("trace_id = %q, want %q", evt.TraceID, "cccc")
	}
}

// The case this whole mechanism exists for: two developers driving the demo
// at the same time. A consumer-query one of them started must not land in the
// other's dev-portal.
func TestWatchNextIgnoresOtherSession(t *testing.T) {
	hub := newTraceHub(time.Minute)

	mine, cleanupMine := hub.watchNext("dev-a", "")
	defer cleanupMine()
	theirs, cleanupTheirs := hub.watchNext("dev-b", "")
	defer cleanupTheirs()

	hub.ingest(taggedBy(dvtpSpan("ffff"), "dev-b"))

	if evt, ok := recv(t, theirs); !ok {
		t.Fatal("dev-b was not woken by its own trace")
	} else if evt.Shared {
		t.Error("shared = true, want false: the trace was attributable")
	}
	mustNotRecv(t, mine, "dev-a")

	// Still armed for its own run.
	hub.ingest(taggedBy(dvtpSpan("gggg"), "dev-a"))
	if evt, ok := recv(t, mine); !ok {
		t.Fatal("dev-a was disarmed by dev-b's trace")
	} else if evt.TraceID != "gggg" {
		t.Errorf("trace_id = %q, want %q", evt.TraceID, "gggg")
	}
}

// A flow nobody could tag — a raw curl, or a browser that never had the
// dev-portal open — still wakes every waiting session. Untagged means
// "unknown", not "someone else's".
func TestWatchNextDeliversUntaggedTraceToSessionWatcher(t *testing.T) {
	hub := newTraceHub(time.Minute)

	ch, cleanup := hub.watchNext("dev-a", "")
	defer cleanup()

	hub.ingest(dvtpSpan("hhhh"))

	if evt, ok := recv(t, ch); !ok {
		t.Fatal("session watcher missed an untagged trace")
	} else if evt.TraceID != "hhhh" {
		t.Errorf("trace_id = %q, want %q", evt.TraceID, "hhhh")
	}
}

// When a trace does go to several sessions at once, every one of them is
// told — the dev-portal cannot say whose run it is, so it says that much.
func TestWatchNextMarksSharedTrace(t *testing.T) {
	hub := newTraceHub(time.Minute)

	first, cleanupFirst := hub.watchNext("", "")
	defer cleanupFirst()
	second, cleanupSecond := hub.watchNext("", "")
	defer cleanupSecond()

	hub.ingest(dvtpSpan("dddd"))

	for name, ch := range map[string]chan watchEvent{"first": first, "second": second} {
		evt, ok := recv(t, ch)
		if !ok {
			t.Fatalf("%s watcher was not woken", name)
		}
		if !evt.Shared {
			t.Errorf("%s watcher: shared = false, want true", name)
		}
	}
}

// A trace announces itself once. A watcher that arms midway through an
// already-running flow waits for the next one instead of being handed a run
// that was underway before it asked.
func TestWatchNextFiresOncePerTrace(t *testing.T) {
	hub := newTraceHub(time.Minute)

	early, cleanupEarly := hub.watchNext("", "")
	defer cleanupEarly()

	hub.ingest(eudiSpan("eeee", "inkomensverklaring_2025"))
	if _, ok := recv(t, early); !ok {
		t.Fatal("watcher was not woken")
	}

	late, cleanupLate := hub.watchNext("", "")
	defer cleanupLate()

	hub.ingest(eudiSpan("eeee", "inkomensverklaring_2025"))
	mustNotRecv(t, late, "watcher armed after the trace started")
}
