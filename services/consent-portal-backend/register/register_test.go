package register

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"gbo-demo/consent-portal-backend/consent"
	"gbo-demo/consent-portal-backend/upstream"
)

// The register's refusals map to the core's errors: 404 is not found, 409 is a
// consent that is no longer active (#600).
func TestRevokeMapsTheRegistersRefusals(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusNotFound, consent.ErrNotFound},
		{http.StatusConflict, consent.ErrNotRevocable},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/consents/c-1" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(server.Close)
			client := Client{Base: server.URL, Caller: upstream.Caller{Client: server.Client()}}

			if err := client.Revoke(context.Background(), "c-1"); !errors.Is(err, tc.want) {
				t.Fatalf("Revoke = %v, want %v", err, tc.want)
			}
		})
	}
}
