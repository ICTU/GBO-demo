package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The ASI tab reaches the QTSP's request API through the backend: method,
// path and body go to the QTSP, its status and body come back unchanged.
func TestAsiRelaysToTheQTSP(t *testing.T) {
	var got []string
	qtsp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+string(body))
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"detail":"no data service"}`)
	}))
	defer qtsp.Close()
	mux := newMux(config{QtspURL: qtsp.URL}, newTraceHub(0), nil)

	for _, tc := range []struct{ method, path, body, want string }{
		{http.MethodGet, "/asi/attributes", "", "GET /api/attributes "},
		{http.MethodPost, "/asi/requests", `{"operation":"verify"}`, `POST /api/requests {"operation":"verify"}`},
		{http.MethodGet, "/asi/requests/abc_-1", "", "GET /api/requests/abc_-1 "},
	} {
		got = nil
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%s %s reached the QTSP as %q, want %q", tc.method, tc.path, got, tc.want)
		}
		if rec.Code != http.StatusUnprocessableEntity || rec.Body.String() != `{"detail":"no data service"}` || rec.Header().Get("Content-Type") != "application/problem+json" {
			t.Errorf("%s %s = %d %q, want the QTSP's answer unchanged", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// Only the three calls of the request API pass; an id is a plain token.
func TestAsiPassesOnlyTheRequestAPI(t *testing.T) {
	reached := false
	qtsp := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer qtsp.Close()
	mux := newMux(config{QtspURL: qtsp.URL}, newTraceHub(0), nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/asi/attributes"},
		{http.MethodDelete, "/asi/requests/abc"},
		{http.MethodGet, "/asi/requests/a%2F..%2Fhealthz"},
		{http.MethodGet, "/asi/callback"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
	if reached {
		t.Error("a call outside the request API reached the QTSP")
	}
}

// Without a QTSP the tab says so; with an unreachable one, 502.
func TestAsiWithoutAQTSP(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux(config{}, newTraceHub(0), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/asi/attributes", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no QTSP configured = %d, want 503", rec.Code)
	}
	rec = httptest.NewRecorder()
	newMux(config{QtspURL: "http://127.0.0.1:1"}, newTraceHub(0), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/asi/attributes", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("QTSP unreachable = %d, want 502", rec.Code)
	}
}
