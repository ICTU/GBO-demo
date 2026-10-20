package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Q8: a request, and its outcome, can be fetched for ten minutes; after
// that the request and its start URL are gone.
func TestARequestExpires(t *testing.T) {
	h := newHandler(nil, DemoAttributes, "http://localhost:4023", nil)
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	h.requests["r1"] = &request{prepared: prepared{operation: "verify"}, expires: now.Add(requestLifetime)}
	h.finish("r1", outcome{Status: statusDone, Operation: "verify"})
	mux := h.routes()
	get := func(path string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}

	now = now.Add(requestLifetime - time.Second)
	if code := get("/api/requests/r1"); code != http.StatusOK {
		t.Fatalf("within the lifetime of the outcome: %d", code)
	}
	now = now.Add(2 * time.Second)
	if code := get("/api/requests/r1"); code != http.StatusNotFound {
		t.Errorf("after the lifetime: %d, want 404", code)
	}
	if code := get("/requests/r1/start"); code != http.StatusNotFound {
		t.Errorf("start after the lifetime: %d, want 404", code)
	}
}
