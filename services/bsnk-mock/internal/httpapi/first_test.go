package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestTheFirstInterfaceRoundTripsABSN(t *testing.T) {
	srv := newTestServer(t, false)

	var pseud struct {
		Pseudonym string `json:"pseudonym"`
		PI        string `json:"pi"`
	}
	ok(t, srv, "/pseudonymize", map[string]string{"bsn": "987654321", "recipient_oin": sourceOIN}, &pseud)
	if pseud.PI == "" || pseud.Pseudonym == "" {
		t.Fatalf("answer = %+v", pseud)
	}

	var resolved struct {
		BSN string `json:"bsn"`
	}
	ok(t, srv, "/transform", map[string]string{"pi": pseud.PI, "recipient_oin": sourceOIN}, &resolved)
	if resolved.BSN != "987654321" {
		t.Errorf("BSN = %q, want 987654321", resolved.BSN)
	}
}

func TestTheFirstInterfaceRefusals(t *testing.T) {
	srv := newTestServer(t, false)
	for name, tc := range map[string]struct {
		path string
		body map[string]string
		want int
	}{
		"pseudonymize without a BSN":    {"/pseudonymize", map[string]string{"recipient_oin": sourceOIN}, http.StatusBadRequest},
		"transform without a recipient": {"/transform", map[string]string{"pi": "PI-0000000000000000"}, http.StatusBadRequest},
		"transform of an unknown PI":    {"/transform", map[string]string{"pi": "PI-0000000000000000", "recipient_oin": sourceOIN}, http.StatusNotFound},
	} {
		status, answer := call(t, srv, tc.path, tc.body)
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(answer, &e)
		if status != tc.want || e.Error == "" {
			t.Errorf("%s: status %d, answer %s; want %d with an error", name, status, answer, tc.want)
		}
	}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t, false)
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
