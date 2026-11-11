package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbo-demo/dev-portal-backend/internal/testpid"
)

type stubIssuer struct {
	links map[string]string
	err   error
}

func (s stubIssuer) Offer(context.Context, testpid.OfferRequest) (map[string]string, error) {
	return s.links, s.err
}

var testPersons = []testpid.Person{{BSN: "999991772", Name: "Frouke Jansen"}}

func testPIDMux(issuer testpid.Issuer) *http.ServeMux {
	mux := newMux(config{}, newTraceHub(0), nil)
	if issuer != nil {
		registerTestPID(mux, testpid.New(testPersons, issuer))
	}
	return mux
}

func testPIDRequest(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

// H1: without the setting there is no test PID endpoint.
func TestTestPIDIsAbsentWithoutTheSetting(t *testing.T) {
	mux := testPIDMux(nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/test-pid/persons"},
		{http.MethodPost, "/test-pid/offers"},
	} {
		if rec := testPIDRequest(mux, tc.method, tc.path, `{"bsn":"999991772"}`); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

// H2: the test persons, by BSN and name.
func TestTestPIDPersons(t *testing.T) {
	rec := testPIDRequest(testPIDMux(stubIssuer{}), http.MethodGet, "/test-pid/persons", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `[{"bsn":"999991772","name":"Frouke Jansen"}]` {
		t.Errorf("persons = %d %s", rec.Code, rec.Body.String())
	}
}

// H3: an offer for a test person gives its link, never from a cache.
func TestTestPIDOffer(t *testing.T) {
	mux := testPIDMux(stubIssuer{links: map[string]string{"openid-credential-offer": "openid-credential-offer://?x"}})
	rec := testPIDRequest(mux, http.MethodPost, "/test-pid/offers", `{"bsn":"999991772"}`)
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != `{"link":"openid-credential-offer://?x"}` {
		t.Errorf("offer = %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
}

// H4: each reason has its status and goes to the portal as its code.
func TestTestPIDOfferReasons(t *testing.T) {
	for _, tc := range []struct {
		bsn    string
		err    error
		status int
		code   string
	}{
		{"123456782", nil, http.StatusBadRequest, "unknown_person"},
		{"999991772", &testpid.Refusal{Status: 404, Code: "no_data"}, http.StatusNotFound, "no_data"},
		{"999991772", &testpid.Refusal{Status: 400, Code: "unknown_configuration"}, http.StatusServiceUnavailable, "test_pid_not_configured"},
		{"999991772", &testpid.Refusal{Status: 503, Code: "source_unavailable"}, http.StatusServiceUnavailable, "source_unavailable"},
		{"999991772", testpid.ErrUnreachable, http.StatusBadGateway, "issuer_unavailable"},
		{"999991772", &testpid.Refusal{Status: 502, Code: "bad_source_data"}, http.StatusBadGateway, "bad_source_data"},
		{"999991772", &testpid.Refusal{Status: 500, Code: "server_error"}, http.StatusBadGateway, "issuer_error"},
	} {
		rec := testPIDRequest(testPIDMux(stubIssuer{err: tc.err}), http.MethodPost, "/test-pid/offers", `{"bsn":"`+tc.bsn+`"}`)
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != tc.status || body["error"] != tc.code {
			t.Errorf("%s with %v = %d %v, want %d %s", tc.bsn, tc.err, rec.Code, body, tc.status, tc.code)
		}
	}
}

// H5 and H6: a request that is not an offer request.
func TestTestPIDRefusesOtherRequests(t *testing.T) {
	mux := testPIDMux(stubIssuer{})
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/test-pid/offers", `{`, http.StatusBadRequest},
		{http.MethodPost, "/test-pid/offers", `{"bsn":""}`, http.StatusBadRequest},
		{http.MethodPost, "/test-pid/offers", ``, http.StatusBadRequest},
		{http.MethodGet, "/test-pid/offers", ``, http.StatusMethodNotAllowed},
		{http.MethodPost, "/test-pid/persons", ``, http.StatusMethodNotAllowed},
	} {
		if rec := testPIDRequest(mux, tc.method, tc.path, tc.body); rec.Code != tc.status {
			t.Errorf("%s %s %q = %d, want %d", tc.method, tc.path, tc.body, rec.Code, tc.status)
		}
	}
}
