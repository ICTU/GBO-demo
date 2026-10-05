package httpapi

import (
	"net/http"
	"testing"
)

// The first interface of the mock is gone: a party reads the value made for
// it with its own keys, and the portal derives its own reference.
func TestTheFirstInterfaceIsGone(t *testing.T) {
	srv := newTestServer(t, false)
	for _, path := range []string{"/pseudonymize", "/transform"} {
		if status, _ := call(t, srv, path, map[string]string{"bsn": "987654321", "recipient_oin": sourceOIN}); status != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", path, status)
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
