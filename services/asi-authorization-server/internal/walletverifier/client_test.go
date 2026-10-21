package walletverifier

import (
	"context"
	"encoding/json"
	"errors"
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
	status               string
	sameDeviceStatus     string
	sameDeviceHTTPStatus int
	sessionTypes         []string
	disclosed            string
	disclosedNonce       string
	disclosedError       string
	// disclosedStatus, when set, answers the disclosed attributes with it.
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
		f.started = map[string]any{}
		_ = json.Unmarshal(raw, &f.started)
		_, _ = io.WriteString(w, `{"session_token":"s1"}`)
	}))
	mux.HandleFunc("GET /disclosure/sessions/s1", func(w http.ResponseWriter, r *http.Request) {
		sessionType := r.URL.Query().Get("session_type")
		f.sessionTypes = append(f.sessionTypes, sessionType)
		if sessionType != "cross_device" && sessionType != "same_device" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if sessionType == "same_device" && f.sameDeviceStatus != "" {
			_, _ = io.WriteString(w, f.sameDeviceStatus)
			return
		}
		if sessionType == "same_device" && f.sameDeviceHTTPStatus != 0 {
			w.WriteHeader(f.sameDeviceHTTPStatus)
			return
		}
		_, _ = io.WriteString(w, f.status)
	})
	mux.HandleFunc("GET /disclosure/sessions/s1/disclosed_attributes", internal(func(w http.ResponseWriter, r *http.Request) {
		f.disclosedNonce = r.URL.Query().Get("nonce")
		if f.disclosedStatus != 0 {
			w.WriteHeader(f.disclosedStatus)
			_, _ = io.WriteString(w, f.disclosedError)
			return
		}
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
	returnURL := "https://as.test/authorize/wallet/return?transaction=tx-1&session_token={session_token}"
	session, err := New(srv.URL, srv.URL, "secret").Start(context.Background(), returnURL)
	if err != nil || session != "s1" {
		t.Fatalf("Start = %q, %v", session, err)
	}
	raw, _ := json.Marshal(f.started)
	for _, want := range []string{`"usecase":"asi-login"`, `"vct_values":["urn:eudi:pid:nl:1"]`, `"path":["bsn"]`, `"path":["nationalities"]`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("start request lacks %s: %s", want, raw)
		}
	}
	if f.started["return_url_template"] != returnURL {
		t.Errorf("return_url_template = %v, want %q", f.started["return_url_template"], returnURL)
	}
	if _, err := New(srv.URL, srv.URL, "secret").Start(context.Background(), ""); err != nil {
		t.Fatalf("Start without a return URL = %v", err)
	}
	if _, present := f.started["return_url_template"]; present {
		t.Errorf("local cross-device start carries a return_url_template: %v", f.started)
	}
	if _, err := New(srv.URL, srv.URL, "wrong").Start(context.Background(), returnURL); err == nil {
		t.Error("a start without the API key succeeded")
	}
	if _, err := New("http://127.0.0.1:1", srv.URL, "secret").Start(context.Background(), returnURL); err == nil {
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
		`{"status":"CREATED","ul":"https://app.example/deeplink?x=1"}`: {State: authorization.SessionWaiting, Link: "https://app.example/deeplink?x=1", SameDeviceLink: "https://app.example/deeplink?x=1"},
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

// Y1: while a session is new, the adapter obtains the universal links for
// both the QR-code and same-device variants from the verifier status API.
func TestProgressReturnsBothDeviceLinks(t *testing.T) {
	f := &fakeVerifier{
		status:           `{"status":"CREATED","ul":"https://app.example/deeplink/cross"}`,
		sameDeviceStatus: `{"status":"CREATED","ul":"https://app.example/deeplink/same"}`,
	}
	c := New(f.server(t).URL, "", "secret")
	c.Public = c.Internal
	want := authorization.Progress{
		State:          authorization.SessionWaiting,
		Link:           "https://app.example/deeplink/cross",
		SameDeviceLink: "https://app.example/deeplink/same",
	}
	if got, err := c.Progress(context.Background(), "s1"); err != nil || got != want {
		t.Fatalf("Progress = %+v, %v; want %+v", got, err, want)
	}
	if strings.Join(f.sessionTypes, ",") != "cross_device,same_device" {
		t.Errorf("session types requested = %v, want cross_device then same_device", f.sessionTypes)
	}
}

func TestProgressKeepsCrossDeviceLinkWhenSameDeviceStatusFails(t *testing.T) {
	f := &fakeVerifier{
		status:               `{"status":"CREATED","ul":"https://app.example/deeplink/cross"}`,
		sameDeviceHTTPStatus: http.StatusServiceUnavailable,
	}
	c := New(f.server(t).URL, "", "secret")
	c.Public = c.Internal
	want := authorization.Progress{State: authorization.SessionWaiting, Link: "https://app.example/deeplink/cross"}
	if got, err := c.Progress(context.Background(), "s1"); err != nil || got != want {
		t.Fatalf("Progress = %+v, %v; want the working cross-device link %+v", got, err, want)
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
		if _, err := c.Identify(context.Background(), "s1"); err == nil || errors.Is(err, authorization.ErrMeansUnavailable) {
			t.Errorf("%s: Identify = %v, want not identified", name, err)
		}
	}
}

// Y2 and Y4 at the verifier boundary: the verifier's return nonce is sent when
// fetching the PID. A nonce rejection is a failed return, not an unavailable
// verifier.
func TestIdentifyReturn(t *testing.T) {
	f := &fakeVerifier{disclosed: pidDisclosure}
	srv := f.server(t)
	c := New(srv.URL, srv.URL, "secret")
	id, err := c.IdentifyReturn(context.Background(), "s1", "return-nonce")
	if err != nil || id.BSN != "999991772" || f.disclosedNonce != "return-nonce" {
		t.Fatalf("IdentifyReturn = %+v, %v, nonce %q; want the PID fetched with the return nonce", id, err, f.disclosedNonce)
	}
	f.disclosedStatus = http.StatusUnauthorized
	f.disclosedError = `{"type":"nonce","status":401}`
	if _, err := c.IdentifyReturn(context.Background(), "s1", "wrong"); err == nil || errors.Is(err, authorization.ErrMeansUnavailable) {
		t.Fatalf("IdentifyReturn with a wrong nonce = %v, want a failed return, not an unavailable verifier", err)
	}
}

// X5 after the session is done: a verifier that cannot be reached, fails or
// refuses the API key is unavailable; an unknown session does not identify
// the user.
func TestIdentifyWhenTheVerifierFails(t *testing.T) {
	f := &fakeVerifier{disclosed: pidDisclosure}
	srv := f.server(t)
	for status, unavailable := range map[int]bool{http.StatusInternalServerError: true, http.StatusBadGateway: true, http.StatusNotFound: false} {
		f.disclosedStatus = status
		_, err := New(srv.URL, srv.URL, "secret").Identify(context.Background(), "s1")
		if err == nil || errors.Is(err, authorization.ErrMeansUnavailable) != unavailable {
			t.Errorf("disclosed attributes %d: Identify = %v, want unavailable %v", status, err, unavailable)
		}
	}
	f.disclosedStatus = 0
	if _, err := New(srv.URL, srv.URL, "wrong").Identify(context.Background(), "s1"); !errors.Is(err, authorization.ErrMeansUnavailable) {
		t.Errorf("API key refused: Identify = %v, want unavailable", err)
	}
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	if _, err := New(down.URL, down.URL, "secret").Identify(context.Background(), "s1"); !errors.Is(err, authorization.ErrMeansUnavailable) {
		t.Errorf("verifier unreachable: Identify = %v, want unavailable", err)
	}
}
