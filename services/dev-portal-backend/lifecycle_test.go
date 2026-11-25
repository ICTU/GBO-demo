package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeIssuer records what reaches the issuer's demo API and answers with a
// fixed status and body.
type fakeIssuer struct {
	mu                 sync.Mutex
	method, path, body string
	status             int
	answer             string
}

func (f *fakeIssuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.method, f.path, f.body = r.Method, r.URL.EscapedPath(), string(body)
	f.mu.Unlock()
	w.WriteHeader(f.status)
	_, _ = io.WriteString(w, f.answer)
}

func lifecycleMux(issuerURL string) *http.ServeMux {
	mux := http.NewServeMux()
	registerLifecycle(mux, issuerURL)
	return mux
}

// The portal passes every call on to the issuer's demo API as it is, and
// passes the issuer's answer, refusals included, back.
func TestLifecyclePassesOn(t *testing.T) {
	for name, tc := range map[string]struct {
		method, path, body string
		wantPath           string
		status             int
	}{
		"settings":             {http.MethodGet, "/credential-lifecycle/settings", "", "/demo/settings", http.StatusOK},
		"events":               {http.MethodGet, "/credential-lifecycle/events", "", "/demo/events", http.StatusOK},
		"provider batch size":  {http.MethodPut, "/credential-lifecycle/settings/providers/belastingdienst", `{"batch_size":6}`, "/demo/settings/providers/belastingdienst", http.StatusOK},
		"reset provider":       {http.MethodDelete, "/credential-lifecycle/settings/providers/test-pid", "", "/demo/settings/providers/test-pid", http.StatusOK},
		"reuse policy refused": {http.MethodPut, "/credential-lifecycle/settings/configurations/belastingdienst-inkomensverklaring_2024", `{"reuse_policy":{"method":"once_only","batch_size":1}}`, "/demo/settings/configurations/belastingdienst-inkomensverklaring_2024", http.StatusBadRequest},
		"reset configuration":  {http.MethodDelete, "/credential-lifecycle/settings/configurations/test-pid-pid", "", "/demo/settings/configurations/test-pid-pid", http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			issuer := &fakeIssuer{status: tc.status, answer: `{"from":"issuer"}`}
			srv := httptest.NewServer(issuer)
			defer srv.Close()
			rec := httptest.NewRecorder()
			lifecycleMux(srv.URL).ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if issuer.method != tc.method || issuer.path != tc.wantPath || issuer.body != tc.body {
				t.Errorf("issuer got %s %s %q, want %s %s %q", issuer.method, issuer.path, issuer.body, tc.method, tc.wantPath, tc.body)
			}
			if rec.Code != tc.status || rec.Body.String() != `{"from":"issuer"}` {
				t.Errorf("portal answered %d %s, want the issuer's %d answer", rec.Code, rec.Body.String(), tc.status)
			}
		})
	}
}

func TestLifecycleIssuerUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	rec := httptest.NewRecorder()
	lifecycleMux(srv.URL).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/credential-lifecycle/settings", nil))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "issuer_unavailable") {
		t.Errorf("%d %s, want 502 issuer_unavailable", rec.Code, rec.Body.String())
	}
}

// Without the issuer's internal address the routes do not exist, so the
// portal hides its block.
func TestLifecycleOff(t *testing.T) {
	rec := httptest.NewRecorder()
	lifecycleMux("").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/credential-lifecycle/settings", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("%d, want 404", rec.Code)
	}
}
