package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"gbo-demo/ldv-client/ldvtest"
)

// The status lookup is reached over FSC. A request the Inway did not
// authenticate carries no peer, and gets no status.
func TestAStatusLookupWithoutAnFSCPeerIsRefused(t *testing.T) {
	url, store, client := registerUnderTest(t, ldvtest.New(t, allGBOActivities()...))
	consentID := grant(t, url)

	response := getStatus(t, statusUnderTest(t, store, client), consentID, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func TestAStatusLookupTellsARevocation(t *testing.T) {
	url, store, client := registerUnderTest(t, ldvtest.New(t, allGBOActivities()...))
	consentID := grant(t, url)
	statusURL := statusUnderTest(t, store, client)

	request, err := http.NewRequest(http.MethodDelete, url+"/consents/"+consentID, nil)
	if err != nil {
		t.Fatalf("build delete: %v", err)
	}
	revoked, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	_ = revoked.Body.Close()

	var body struct {
		Status string `json:"status"`
	}
	response := getStatus(t, statusURL, consentID, testPDPPeer)
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "REVOKED" {
		t.Fatalf("status = %q, want REVOKED", body.Status)
	}
}

// An Inway forwards every path under the service's endpoint. The status
// listener must therefore serve nothing but the status: not a consent's
// detail, not a citizen's list, not a revocation.
func TestTheStatusListenerServesOnlyTheStatus(t *testing.T) {
	url, store, client := registerUnderTest(t, ldvtest.New(t, allGBOActivities()...))
	consentID := grant(t, url)
	statusURL := statusUnderTest(t, store, client)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/consents/" + consentID},
		{http.MethodGet, "/consents?subject_ref=" + testSubjectRef},
		{http.MethodPost, "/consents"},
		{http.MethodDelete, "/consents/" + consentID},
		{http.MethodDelete, "/consents/" + consentID + "/status"},
		{http.MethodGet, "/.well-known/jwks.json"},
	} {
		request, err := http.NewRequest(tc.method, statusURL+tc.path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		request.Header.Set("Fsc-Authorization", fscAuthorization(t, testPDPPeer))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode < 400 {
			t.Errorf("%s %s = %d, want refused", tc.method, tc.path, response.StatusCode)
		}
	}
}

// The portal's listener no longer answers the status lookup, so it cannot be
// asked around FSC.
func TestThePortalListenerDoesNotServeTheStatus(t *testing.T) {
	url, _, _ := registerUnderTest(t, ldvtest.New(t, allGBOActivities()...))
	consentID := grant(t, url)

	response, err := http.Get(url + "/consents/" + consentID + "/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}
