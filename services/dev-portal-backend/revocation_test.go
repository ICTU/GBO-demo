package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"gbo-demo/dev-portal-backend/internal/revocation"
	"gbo-demo/dev-portal-backend/internal/testpid"
)

type stubRevoker struct {
	count int
	err   error
}

func (s stubRevoker) RevokeReference(context.Context, string) (int, error) { return s.count, s.err }

func (s stubRevoker) RevokeEntry(context.Context, string, string, int) error { return s.err }

type stubCatalog struct{}

func (stubCatalog) Offers(context.Context) ([]revocation.AttestationOffer, error) {
	return []revocation.AttestationOffer{{Key: "inkomensverklaring_2024", Label: "Inkomensverklaring 2024",
		SourceID: "belastingdienst", TypeID: "inkomensverklaring"}}, nil
}

func revocationMux(issuer stubIssuer, revoker stubRevoker) (*http.ServeMux, *revocation.Service) {
	svc := revocation.New(revocation.Config{
		Issuer: issuer, Revoker: revoker, Catalog: stubCatalog{}, StatusBaseURL: "http://issuer:8080",
		Fetch:        func(context.Context, string) (string, error) { return "", context.DeadlineExceeded },
		Now:          func() time.Time { return time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC) },
		NewReference: func() string { return "devportal-1" },
	})
	mux := newMux(config{}, newTraceHub(0), nil)
	registerTestPID(mux, testpid.New(testPersons, issuer), svc)
	registerRevocation(mux, svc)
	return mux, svc
}

var offerLinks = map[string]string{"openid-credential-offer": "openid-credential-offer://?x"}

// B12: without the setting there is no revocation endpoint. A test PID
// offer still carries a reference (TestTestPIDOffer).
func TestRevocationIsAbsentWithoutTheSetting(t *testing.T) {
	mux := testPIDMux(stubIssuer{links: offerLinks})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/revocation/offers"},
		{http.MethodPost, "/revocation/offers"},
		{http.MethodPost, "/revocation/revoke"},
		{http.MethodPost, "/revocation/revoke-entry"},
		{http.MethodGet, "/revocation/status-lists"},
	} {
		if rec := testPIDRequest(mux, tc.method, tc.path, `{}`); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

// B1: with revocation on, a test PID offer carries a reference and is
// listed.
func TestATestPIDOfferIsRevocable(t *testing.T) {
	mux, svc := revocationMux(stubIssuer{links: offerLinks}, stubRevoker{})
	rec := testPIDRequest(mux, http.MethodPost, "/test-pid/offers", `{"bsn":"999991772"}`)
	if rec.Code != http.StatusCreated || strings.TrimSpace(rec.Body.String()) != `{"link":"openid-credential-offer://?x","reference":"devportal-1"}` {
		t.Errorf("offer = %d %s", rec.Code, rec.Body.String())
	}
	if offers := svc.Offers(); len(offers) != 1 || offers[0].Kind != revocation.KindTestPID {
		t.Errorf("offers = %v", offers)
	}
	list := testPIDRequest(mux, http.MethodGet, "/revocation/offers", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"reference":"devportal-1"`) {
		t.Errorf("list = %d %s", list.Code, list.Body.String())
	}
}

// B2 to B9 over HTTP: each outcome has its status and code.
func TestRevocationAnswers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		issuer   stubIssuer
		revoker  stubRevoker
		method   string
		path     string
		body     string
		status   int
		contains string
	}{
		{"attestation offer", stubIssuer{links: offerLinks}, stubRevoker{}, http.MethodPost, "/revocation/offers",
			`{"source_id":"belastingdienst","key":"inkomensverklaring_2024","bsn":"999991772"}`, http.StatusCreated, `"reference":"devportal-1"`},
		{"unknown offer", stubIssuer{links: offerLinks}, stubRevoker{}, http.MethodPost, "/revocation/offers",
			`{"source_id":"belastingdienst","key":"x","bsn":"999991772"}`, http.StatusBadRequest, `"unknown_offer"`},
		{"no data", stubIssuer{err: &testpid.Refusal{Status: 404, Code: "no_data"}}, stubRevoker{}, http.MethodPost, "/revocation/offers",
			`{"source_id":"belastingdienst","key":"inkomensverklaring_2024","bsn":"999991772"}`, http.StatusNotFound, `"no_data"`},
		{"not json", stubIssuer{}, stubRevoker{}, http.MethodPost, "/revocation/revoke", `{`, http.StatusBadRequest, `"invalid_request"`},
		{"unknown reference", stubIssuer{}, stubRevoker{}, http.MethodPost, "/revocation/revoke",
			`{"reference":"devportal-1"}`, http.StatusNotFound, `"unknown_reference"`},
		{"entry", stubIssuer{}, stubRevoker{}, http.MethodPost, "/revocation/revoke-entry",
			`{"provider":"test-pid","list":"pid","index":7}`, http.StatusNoContent, ``},
		{"entry without index", stubIssuer{}, stubRevoker{}, http.MethodPost, "/revocation/revoke-entry",
			`{"provider":"test-pid","list":"pid"}`, http.StatusBadRequest, `"invalid_request"`},
		{"unknown entry", stubIssuer{}, stubRevoker{err: &testpid.Refusal{Status: 404, Code: "unknown_entry"}}, http.MethodPost, "/revocation/revoke-entry",
			`{"provider":"test-pid","list":"pid","index":70000}`, http.StatusNotFound, `"unknown_entry"`},
		{"issuer down", stubIssuer{}, stubRevoker{err: testpid.ErrUnreachable}, http.MethodPost, "/revocation/revoke-entry",
			`{"provider":"test-pid","list":"pid","index":7}`, http.StatusBadGateway, `"issuer_unavailable"`},
		{"status lists", stubIssuer{}, stubRevoker{}, http.MethodGet, "/revocation/status-lists", ``, http.StatusOK, `[]`},
	} {
		mux, _ := revocationMux(tc.issuer, tc.revoker)
		rec := testPIDRequest(mux, tc.method, tc.path, tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("%s = %d %s, want %d with %s", tc.name, rec.Code, rec.Body.String(), tc.status, tc.contains)
		}
	}
}

// B5 over HTTP: a revoked offer comes back with its count.
func TestRevokingAnOfferOverHTTP(t *testing.T) {
	mux, _ := revocationMux(stubIssuer{links: offerLinks}, stubRevoker{count: 4})
	testPIDRequest(mux, http.MethodPost, "/test-pid/offers", `{"bsn":"999991772"}`)
	rec := testPIDRequest(mux, http.MethodPost, "/revocation/revoke", `{"reference":"devportal-1"}`)
	var answer struct {
		Offer revocation.Offer `json:"offer"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &answer)
	if rec.Code != http.StatusOK || answer.Offer.RevokedEntries != 4 || answer.Offer.RevokedAt == nil {
		t.Errorf("revoke = %d %s", rec.Code, rec.Body.String())
	}
}
