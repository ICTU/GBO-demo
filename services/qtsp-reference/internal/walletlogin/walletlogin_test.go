package walletlogin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"gbo-demo/qtsp-reference/internal/login"
)

// fakeVerifier answers like nl-wallet's verification_server: the internal
// API behind the API key, the status on the wallet API.
type fakeVerifier struct {
	status          string
	disclosed       string
	disclosedStatus int
	started         map[string]any
}

func (f *fakeVerifier) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	internal := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /disclosure/sessions", internal(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &f.started)
		_, _ = io.WriteString(w, `{"session_token":"s1"}`)
	}))
	mux.HandleFunc("GET /disclosure/sessions/s1", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("session_type") != "cross_device" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, f.status)
	})
	mux.HandleFunc("GET /disclosure/sessions/s1/disclosed_attributes", internal(func(w http.ResponseWriter, _ *http.Request) {
		if f.disclosedStatus != 0 {
			w.WriteHeader(f.disclosedStatus)
			return
		}
		_, _ = io.WriteString(w, f.disclosed)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const pidDisclosure = `[{"id":"pid","attestations":[{"attestation_type":"urn:eudi:pid:nl:1","format":"dc+sd-jwt",
 "attributes":{"family_name":"Jansen","given_name":"Frouke","birthdate":"2000-03-24","nationalities":["NL","BE"]},
 "issuer_uri":"https://pid.example","attestation_qualification":"PID","ca":"issuance-ca","issuance_validity":{}}]}]`

// The session asks usecase qtsp-login for the four PID attributes, not the
// BSN, with the API key.
func TestStart(t *testing.T) {
	f := &fakeVerifier{}
	srv := f.server(t)
	session, err := New(srv.URL, srv.URL, "secret").Start(context.Background())
	if err != nil || session != "s1" {
		t.Fatalf("Start = %q, %v", session, err)
	}
	raw, _ := json.Marshal(f.started)
	if f.started["usecase"] != Usecase || strings.Contains(string(raw), "bsn") {
		t.Errorf("session request = %s; want usecase %s and no BSN", raw, Usecase)
	}
	for _, c := range Claims {
		if !strings.Contains(string(raw), `"path":["`+c+`"]`) {
			t.Errorf("session request lacks claim %s: %s", c, raw)
		}
	}
	if _, err := New(srv.URL, srv.URL, "wrong").Start(context.Background()); !errors.Is(err, login.ErrUnavailable) {
		t.Errorf("Start with a refused API key = %v, want unavailable", err)
	}
}

// The status of the session, cross-device, as progress.
func TestProgress(t *testing.T) {
	f := &fakeVerifier{}
	srv := f.server(t)
	c := New(srv.URL, srv.URL, "secret")
	for status, want := range map[string]login.Progress{
		`{"status":"CREATED","ul":"https://app.example/deeplink?x=1"}`: {State: login.Waiting, Link: "https://app.example/deeplink?x=1"},
		`{"status":"WAITING_FOR_RESPONSE"}`:                            {State: login.Waiting},
		`{"status":"DONE"}`:                                            {State: login.Done},
		`{"status":"FAILED"}`:                                          {State: login.Failed},
		`{"status":"CANCELLED"}`:                                       {State: login.Failed},
		`{"status":"EXPIRED"}`:                                         {State: login.Failed},
	} {
		f.status = status
		if got, err := c.Progress(context.Background(), "s1"); err != nil || got != want {
			t.Errorf("%s: Progress = %+v, %v; want %+v", status, got, err, want)
		}
	}
}

// T2 and T5: the PID with the four attributes identifies the citizen;
// anything else does not, and is not "unavailable".
func TestIdentify(t *testing.T) {
	f := &fakeVerifier{disclosed: pidDisclosure}
	srv := f.server(t)
	c := New(srv.URL, srv.URL, "secret")
	id, err := c.Identify(context.Background(), "s1")
	if err != nil || id.FamilyName != "Jansen" || id.GivenName != "Frouke" || id.Birthdate != "2000-03-24" ||
		!slices.Equal(id.Nationalities, []string{"NL", "BE"}) || id.Means != login.MeansWallet {
		t.Fatalf("Identify = %+v, %v", id, err)
	}
	for name, disclosed := range map[string]string{
		"no family name":   strings.Replace(pidDisclosure, `"family_name":"Jansen",`, ``, 1),
		"no nationalities": strings.Replace(pidDisclosure, `,"nationalities":["NL","BE"]`, ``, 1),
		"another type":     strings.Replace(pidDisclosure, `urn:eudi:pid:nl:1`, `urn:eudi:pid:1`, 1),
		"another query id": strings.Replace(pidDisclosure, `"id":"pid"`, `"id":"other"`, 1),
		"nothing":          `[]`,
	} {
		f.disclosed = disclosed
		if _, err := c.Identify(context.Background(), "s1"); err == nil || errors.Is(err, login.ErrUnavailable) {
			t.Errorf("%s: Identify = %v, want not identified", name, err)
		}
	}
}

// T4: a verifier that cannot be reached, fails or refuses the API key is
// unavailable; an unknown session does not identify the citizen.
func TestIdentifyWhenTheVerifierFails(t *testing.T) {
	f := &fakeVerifier{disclosed: pidDisclosure}
	srv := f.server(t)
	for status, unavailable := range map[int]bool{http.StatusInternalServerError: true, http.StatusBadGateway: true, http.StatusNotFound: false} {
		f.disclosedStatus = status
		_, err := New(srv.URL, srv.URL, "secret").Identify(context.Background(), "s1")
		if err == nil || errors.Is(err, login.ErrUnavailable) != unavailable {
			t.Errorf("disclosed attributes %d: Identify = %v, want unavailable %v", status, err, unavailable)
		}
	}
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	if _, err := New(down.URL, down.URL, "secret").Identify(context.Background(), "s1"); !errors.Is(err, login.ErrUnavailable) {
		t.Errorf("verifier unreachable: Identify = %v, want unavailable", err)
	}
}
