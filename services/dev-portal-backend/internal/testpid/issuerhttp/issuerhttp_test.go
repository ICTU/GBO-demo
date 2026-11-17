package issuerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"gbo-demo/dev-portal-backend/internal/testpid"
)

var request = testpid.OfferRequest{Profile: "eudi", ConfigurationIDs: []string{"test-pid-pid"}, BSN: "999991772"}

// A1 and A2: the offer request as the issuer's internal API takes it, and
// the links from its answer.
func TestTheOfferRequestAndItsLinks(t *testing.T) {
	var gotPath, gotType string
	var gotBody map[string]any
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotType = r.Method+" "+r.URL.Path, r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"credential_offer":{},"links":{"openid-credential-offer":"openid-credential-offer://?x"}}`)
	}))
	defer issuer.Close()

	links, err := New(issuer.URL+"/offers", time.Second).Offer(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "POST /offers" || gotType != "application/json" {
		t.Errorf("request = %s with %q", gotPath, gotType)
	}
	wantBody := map[string]any{
		"profile":                      "eudi",
		"credential_configuration_ids": []any{"test-pid-pid"},
		"subject":                      map[string]any{"bsn": "999991772"},
		"parameters":                   map[string]any{},
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("body = %v, want %v", gotBody, wantBody)
	}
	if links["openid-credential-offer"] != "openid-credential-offer://?x" {
		t.Errorf("links = %v", links)
	}
}

// A3 and A5: an error answer is a refusal with the issuer's code, or with
// none when the answer is not the issuer's JSON.
func TestAnErrorAnswerIsARefusal(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   testpid.Refusal
	}{
		{http.StatusNotFound, `{"error":"no_data","error_description":"no data"}`, testpid.Refusal{Status: 404, Code: "no_data"}},
		{http.StatusBadRequest, `{"error":"unknown_configuration"}`, testpid.Refusal{Status: 400, Code: "unknown_configuration"}},
		{http.StatusBadGateway, `<html>bad gateway</html>`, testpid.Refusal{Status: 502}},
		{http.StatusInternalServerError, ``, testpid.Refusal{Status: 500}},
	} {
		issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		_, err := New(issuer.URL, time.Second).Offer(context.Background(), request)
		issuer.Close()
		var refusal *testpid.Refusal
		if !errors.As(err, &refusal) || *refusal != tc.want {
			t.Errorf("%d %q: err = %v, want %v", tc.status, tc.body, err, tc.want)
		}
	}
}

// A4: an issuer that refuses the connection or does not answer in time is
// unreachable.
func TestAnIssuerThatDoesNotAnswerIsUnreachable(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)

	for name, url := range map[string]string{"refused": closed.URL, "timeout": slow.URL} {
		_, err := New(url, 50*time.Millisecond).Offer(context.Background(), request)
		if !errors.Is(err, testpid.ErrUnreachable) {
			t.Errorf("%s: err = %v, want unreachable", name, err)
		}
	}
}

// B1 and B2: the reference and the parameters go in the offer request.
func TestTheOfferRequestCarriesReferenceAndParameters(t *testing.T) {
	var gotBody map[string]any
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"links":{"openid-credential-offer":"openid-credential-offer://?x"}}`)
	}))
	defer issuer.Close()
	req := request
	req.Parameters, req.Reference = map[string]string{"belastingjaar": "2024"}, "devportal-1"
	if _, err := New(issuer.URL+"/offers", time.Second).Offer(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if gotBody["reference"] != "devportal-1" || !reflect.DeepEqual(gotBody["parameters"], map[string]any{"belastingjaar": "2024"}) {
		t.Errorf("body = %v", gotBody)
	}
}

// B5 and B7: a revocation by reference returns the count; one by entry
// names provider, list and index.
func TestRevocations(t *testing.T) {
	var bodies []map[string]any
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path != "POST /revocations" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		bodies = append(bodies, body)
		if _, ok := body["reference"]; ok {
			_, _ = io.WriteString(w, `{"revoked":4}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer issuer.Close()
	c := NewRevocations(issuer.URL+"/revocations", time.Second)
	n, err := c.RevokeReference(context.Background(), "devportal-1")
	if err != nil || n != 4 {
		t.Fatalf("revoked %d, %v; want 4", n, err)
	}
	if err := c.RevokeEntry(context.Background(), "test-pid", "pid", 7); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"reference": "devportal-1"}, {"provider": "test-pid", "list": "pid", "index": float64(7)}}
	if !reflect.DeepEqual(bodies, want) {
		t.Errorf("bodies = %v, want %v", bodies, want)
	}
}

// B7 and B8: a refusal carries the issuer's code; no answer is
// ErrUnreachable.
func TestARevocationRefusal(t *testing.T) {
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"unknown_entry"}`)
	}))
	err := NewRevocations(issuer.URL, time.Second).RevokeEntry(context.Background(), "test-pid", "pid", 7)
	var refusal *testpid.Refusal
	if !errors.As(err, &refusal) || *refusal != (testpid.Refusal{Status: 404, Code: "unknown_entry"}) {
		t.Errorf("err = %v, want a refusal unknown_entry", err)
	}
	issuer.Close()
	if _, err := NewRevocations(issuer.URL, time.Second).RevokeReference(context.Background(), "x"); !errors.Is(err, testpid.ErrUnreachable) {
		t.Errorf("err = %v, want ErrUnreachable", err)
	}
}
