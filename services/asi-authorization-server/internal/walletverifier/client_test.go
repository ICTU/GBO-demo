package walletverifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gbo-demo/asi-authorization-server/internal/authorization"
)

// fakeVerifier answers like nl-wallet's verification_server: the internal
// API behind the API key, the status on the wallet API.
type fakeVerifier struct {
	status    string
	disclosed string
	started   map[string]any
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
		_, _ = io.WriteString(w, f.disclosed)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

const pidDisclosure = `[{"id":"pid","attestations":[{"attestation_type":"urn:eudi:pid:nl:1","format":"dc+sd-jwt",
 "attributes":{"bsn":"999991772","family_name":"Jansen","given_name":"Frouke","birthdate":"2000-03-24","nationalities":["NL","BE"]},
 "issuer_uri":"https://pid.example","attestation_qualification":"PID","ca":"issuance-ca","issuance_validity":{}}]}]`

// X1: a session for usecase asi-login that asks the PID claims.
func TestStart(t *testing.T) {
	f := &fakeVerifier{}
	srv := f.server(t)
	session, err := New(srv.URL, srv.URL, "secret").Start(context.Background())
	if err != nil || session != "s1" {
		t.Fatalf("Start = %q, %v", session, err)
	}
	raw, _ := json.Marshal(f.started)
	for _, want := range []string{`"usecase":"asi-login"`, `"vct_values":["urn:eudi:pid:nl:1"]`, `"path":["bsn"]`, `"path":["nationalities"]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("start request lacks %s: %s", want, raw)
		}
	}
	if _, err := New(srv.URL, srv.URL, "wrong").Start(context.Background()); err == nil {
		t.Error("a start without the API key succeeded")
	}
	if _, err := New("http://127.0.0.1:1", srv.URL, "secret").Start(context.Background()); err == nil {
		t.Error("a start at an unreachable verifier succeeded")
	}
}

// The status of the session, cross-device, as progress: waiting (with the
// link of the QR code while there is one), done, or failed (X4).
func TestProgress(t *testing.T) {
	f := &fakeVerifier{}
	c := New(f.server(t).URL, "", "secret")
	c.Public = c.Internal
	for status, want := range map[string]authorization.Progress{
		`{"status":"CREATED","ul":"https://app.example/deeplink?x=1"}`: {State: authorization.SessionWaiting, Link: "https://app.example/deeplink?x=1"},
		`{"status":"WAITING_FOR_RESPONSE"}`:                            {State: authorization.SessionWaiting},
		`{"status":"DONE"}`:                                            {State: authorization.SessionDone},
		`{"status":"FAILED"}`:                                          {State: authorization.SessionFailed},
		`{"status":"CANCELLED"}`:                                       {State: authorization.SessionFailed},
		`{"status":"EXPIRED"}`:                                         {State: authorization.SessionFailed},
	} {
		f.status = status
		if got, err := c.Progress(context.Background(), "s1"); err != nil || got != want {
			t.Errorf("%s: Progress = %+v, %v; want %+v", status, got, err, want)
		}
	}
}

// X2: the disclosed PID is the identity; X6: without a BSN, or of another
// credential type, it does not identify the user.
func TestIdentify(t *testing.T) {
	f := &fakeVerifier{disclosed: pidDisclosure}
	srv := f.server(t)
	c := New(srv.URL, srv.URL, "secret")
	id, err := c.Identify(context.Background(), "s1")
	if err != nil || id.BSN != "999991772" || id.FamilyName != "Jansen" || id.GivenName != "Frouke" || id.Birthdate != "2000-03-24" || len(id.Nationalities) != 2 || id.Means != "" {
		t.Fatalf("Identify = %+v, %v; want the PID, and no means (the core names it)", id, err)
	}
	for name, disclosed := range map[string]string{
		"no BSN":           strings.Replace(pidDisclosure, `"bsn":"999991772",`, ``, 1),
		"another type":     strings.Replace(pidDisclosure, `urn:eudi:pid:nl:1`, `urn:eudi:pid:1`, 1),
		"another query id": strings.Replace(pidDisclosure, `"id":"pid"`, `"id":"other"`, 1),
		"nothing":          `[]`,
	} {
		f.disclosed = disclosed
		if _, err := c.Identify(context.Background(), "s1"); err == nil {
			t.Errorf("%s: identified the user", name)
		}
	}
}
