package authorization

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeRegister is an in-memory register.
type fakeRegister struct {
	entries     map[string]RegisterEntry
	unavailable bool
}

func (f *fakeRegister) Lookup(_ context.Context, uri string) (RegisterEntry, error) {
	if f.unavailable {
		return RegisterEntry{}, ErrRegisterUnavailable
	}
	e, ok := f.entries[uri]
	if !ok {
		return RegisterEntry{}, ErrNotRegistered
	}
	return e, nil
}

// The approval of a registry entry: present, QEAA_Provider, and naming the
// organisation of the certificate.
func TestApprove(t *testing.T) {
	reg := &fakeRegister{entries: map[string]RegisterEntry{
		"qtsp":     {Identifiers: []string{"NTRNL-1"}, LegalName: "Test QTSP", Entitlements: []string{QEAAProviderEntitlement}},
		"provider": {Identifiers: []string{"NTRNL-1"}, Entitlements: []string{"https://uri.etsi.org/19475/Entitlement/Service_Provider"}},
		"other":    {Identifiers: []string{"NTRNL-2"}, Entitlements: []string{QEAAProviderEntitlement}},
		"data-eu":  {Identifiers: []string{"NTRNL-1"}, Entitlements: []string{"http://data.europa.eu/eudi/entitlement/QEAA_Provider"}},
	}}
	s := NewService(Config{Register: reg})
	// The entry gives the legal name the user sees (F in #544).
	if entry, err := s.approve(context.Background(), "qtsp", "NTRNL-1"); err != nil || entry.LegalName != "Test QTSP" {
		t.Fatalf("approve = %+v, %v; want approved with the entry", entry, err)
	}
	for _, tc := range []struct{ name, uri, reason string }{
		{"unknown registryURI", "unknown", "not in the register"},
		{"no QEAA_Provider", "provider", "no QEAA_Provider entitlement"},
		{"another organisation", "other", "organizationIdentifier"},
		{"entitlement in the other URI form of TS5", "data-eu", "no QEAA_Provider entitlement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.approve(context.Background(), tc.uri, "NTRNL-1")
			if err == nil || err.Code != "unapproved_software_statement" || !strings.Contains(err.Description, tc.reason) {
				t.Fatalf("approve = %v, want unapproved_software_statement: %s", err, tc.reason)
			}
		})
	}
	reg.unavailable = true
	if _, err := s.approve(context.Background(), "qtsp", "NTRNL-1"); err == nil || err.Code != "temporarily_unavailable" {
		t.Fatalf("approve with the register unavailable = %v, want temporarily_unavailable", err)
	}
	if _, err := NewService(Config{}).approve(context.Background(), "qtsp", "NTRNL-1"); err == nil || err.Code != "temporarily_unavailable" {
		t.Fatalf("approve without a register = %v, want temporarily_unavailable", err)
	}
}

// Re-verification deactivates a client that is no longer approved and keeps
// one when the register is unavailable; clients from configuration are not
// re-verified.
func TestRecheckEntitlements(t *testing.T) {
	reg := &fakeRegister{entries: map[string]RegisterEntry{
		"a": {Identifiers: []string{"NTRNL-1"}, Entitlements: []string{QEAAProviderEntitlement}},
		"b": {Identifiers: []string{"NTRNL-2"}, Entitlements: []string{QEAAProviderEntitlement}},
	}}
	s := NewService(Config{Register: reg, Clients: fakeClients{}})
	for id, uri := range map[string]string{"client-a": "a", "client-b": "b"} {
		org := map[string]string{"a": "NTRNL-1", "b": "NTRNL-2"}[uri]
		s.registered.add(&registeredClient{client: &Client{ID: id}, registryURI: uri, organizationID: org, active: true})
	}
	delete(reg.entries, "b")
	if res := s.RecheckEntitlements(context.Background()); res != (RecheckResult{Checked: 2, Deactivated: 1}) {
		t.Fatalf("recheck = %+v, want 2 checked, 1 deactivated", res)
	}
	if _, ok := s.client("client-a"); !ok {
		t.Fatalf("client-a was deactivated")
	}
	if _, ok := s.client("client-b"); ok {
		t.Fatalf("client-b is still active")
	}
	reg.unavailable = true
	if res := s.RecheckEntitlements(context.Background()); res != (RecheckResult{Checked: 1, Unavailable: 1}) {
		t.Fatalf("recheck with the register unavailable = %+v, want 1 checked, 1 unavailable", res)
	}
	if _, ok := s.client("client-a"); !ok {
		t.Fatalf("client-a was deactivated while the register was unavailable")
	}
}

// Without trust anchors no certificate is trusted, not even through the
// system roots.
func TestRegistrationWithoutTrustAnchors(t *testing.T) {
	s := NewService(Config{Register: &fakeRegister{}, Now: time.Now})
	_, err := s.Register(context.Background(), RegistrationRequest{SoftwareStatement: "x.y.z"})
	if err == nil || err.Code != "invalid_software_statement" || !strings.Contains(err.Description, "trust anchors") {
		t.Fatalf("Register = %v, want invalid_software_statement about trust anchors", err)
	}
}
