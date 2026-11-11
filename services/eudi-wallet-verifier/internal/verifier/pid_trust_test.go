package verifier_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"gbo-demo/eudi-wallet-verifier/internal/verifier"
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
func (p *currentPIDTrust) set(trust x5c.Trust, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trust = trust
	p.err = err
}

// I4, I6, I7: use current trust even for an ongoing session; a trust
// outage is temporary and leaves the response available for retry.
func TestCentralPIDTrust(t *testing.T) {
	trust := &currentPIDTrust{}
	var original x5c.Trust
	f := newFixture(t, func(f *fixture, cfg *verifier.Config) {
		original = x5c.Trust{Anchors: cfg.PIDAnchors, Authorities: cfg.PIDTrustedAuthorities}
		trust.set(original, nil)
		cfg.PIDAnchors = nil
		cfg.PIDTrustedAuthorities = nil
		cfg.PIDTrust = trust
	})
	token := f.mustStart(t, "qtsp-login", "")
	r := f.fetch(t, token, verifier.CrossDevice)
	form := f.present(t, r, f.pid(t, f.issuer, nil), loginClaims[1:]...)
	trust.set(x5c.Trust{Anchors: []x5c.Anchors{x5c.NewAnchors("replacement", testpki.NewRoot(t, "replacement").Cert)}}, nil)
	if status, _ := f.post(t, r, form); status != http.StatusBadRequest {
		t.Fatalf("removed root accepted: %d", status)
	}
	trust.set(original, nil)
	token = f.mustStart(t, "qtsp-login", "")
	r = f.fetch(t, token, verifier.CrossDevice)
	form = f.present(t, r, f.pid(t, f.issuer, nil), loginClaims[1:]...)
	trust.set(x5c.Trust{}, errors.New("no valid list"))
	if status, body := f.post(t, r, form); status != http.StatusServiceUnavailable || body["error"] != "server_error" {
		t.Fatalf("unavailable: %d %v", status, body)
	}
	if status := f.status(t, token, verifier.CrossDevice).Status; status != string(verifier.StatusWaiting) {
		t.Fatalf("outage consumed session: %s", status)
	}
	trust.set(original, nil)
	if status, body := f.post(t, r, form); status != http.StatusOK {
		t.Fatalf("recovery: %d %v", status, body)
	}
	if status := f.status(t, token, verifier.CrossDevice).Status; status != string(verifier.StatusDone) {
		t.Fatalf("PID flow after recovery: %s", status)
	}
}
