package httpapi_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"gbo-demo/eudi-credential-issuer/internal/issuance"
	"gbo-demo/oid4vc-kit/testpki"
	"gbo-demo/oid4vc-kit/x5c"
)

type currentPIDTrust struct {
	mu    sync.Mutex
	trust x5c.Trust
	err   error
}

func (p *currentPIDTrust) Load(context.Context) (x5c.Trust, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.trust, p.err
}
func (p *currentPIDTrust) set(anchors []x5c.Anchors, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trust = x5c.Trust{Anchors: anchors}
	p.err = err
}

// I4, I6, I7 at the issuer boundary: a response uses current roots rather
// than the roots at session creation. An outage never consumes the session.
func TestCentralPIDTrust(t *testing.T) {
	for _, profile := range []string{"nl", "eudi"} {
		t.Run(profile, func(t *testing.T) {
			trust := &currentPIDTrust{}
			f := newFixtureWith(t, func(f *fixture, cfg *issuance.Config) {
				trust.set(cfg.PIDAnchors, nil)
				cfg.PIDAnchors = nil
				cfg.PIDTrust = trust
			})
			if status, body := f.disclose(t, profile, "bsn"); status != http.StatusOK {
				t.Fatalf("PID flow: %d %v", status, body)
			}
			_, r := f.fetchRequest(t, "tax", profile, incomeOffer)
			response := f.presentation(t, r, f.pidIssuer, "bsn")
			trust.set([]x5c.Anchors{x5c.NewAnchors("replacement", testpki.NewRoot(t, "replacement").Cert)}, nil)
			status, body := f.respond(t, r, response)
			expectTrust(t, "removed root in ongoing session", status, body, http.StatusBadRequest, "invalid_request")
			trust.set([]x5c.Anchors{x5c.NewAnchors("central", f.pidRoot.Cert)}, nil)
			_, r = f.fetchRequest(t, "tax", profile, incomeOffer)
			response = f.presentation(t, r, f.pidIssuer, "bsn")
			trust.set(nil, errors.New("no valid LoTE"))
			status, body = f.respond(t, r, response)
			expectTrust(t, "unavailable", status, body, http.StatusServiceUnavailable, "server_error")
			if status, _ = f.fetchRequest(t, "tax", profile, incomeOffer); status != http.StatusServiceUnavailable {
				t.Fatalf("unavailable request: %d", status)
			}
			// Unrelated metadata stays available while the PID trust source is down.
			if status, _, _ := get(t, f.public.URL+"/.well-known/openid-credential-issuer/tax/"+profile, ""); status != http.StatusOK {
				t.Fatalf("unrelated metadata blocked: %d", status)
			}
			trust.set([]x5c.Anchors{x5c.NewAnchors("central", f.pidRoot.Cert)}, nil)
			status, body = f.respond(t, r, response)
			expectTrust(t, "same response after recovery", status, body, http.StatusOK, "")
		})
	}
}

func expectTrust(t *testing.T, name string, status int, body map[string]any, want int, code string) {
	t.Helper()
	if status != want || (code != "" && body["error"] != code) {
		t.Fatalf("%s: %d %v, want %d %s", name, status, body, want, code)
	}
}
