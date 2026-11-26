package portalhttp

import (
	"context"
	"net/http"
	"testing"
	"time"

	"gbo-demo/consent-portal-backend/consent"
)

// revokeRegister is a register that holds consents, so the revoke endpoint
// has something to revoke. It counts the revocations it performs.
type revokeRegister struct {
	*fakeRegister
	recs    map[string]consent.Record
	revoked []string
}

func (f *revokeRegister) Get(_ context.Context, consentID string) (consent.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[consentID]
	if !ok {
		return consent.Record{}, consent.ErrNotFound
	}
	return rec, nil
}

func (f *revokeRegister) Revoke(_ context.Context, consentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.recs[consentID]
	if !ok {
		return consent.ErrNotFound
	}
	rec.Status = "REVOKED"
	f.recs[consentID] = rec
	f.revoked = append(f.revoked, consentID)
	return nil
}

// revokeHarness serves the portal over a register with one consent of
// citizenA per status: active, expired and revoked.
func revokeHarness(t *testing.T) (*harness, *revokeRegister) {
	t.Helper()
	h := newHarness(t, returnURL)
	owner, err := h.portal.SubjectRefs.For(citizenA)
	if err != nil {
		t.Fatal(err)
	}
	register := &revokeRegister{fakeRegister: h.register, recs: map[string]consent.Record{
		"c-active":  {ID: "c-active", SubjectRef: owner, Status: "ACTIVE", ValidUntil: h.now.Add(time.Hour)},
		"c-expired": {ID: "c-expired", SubjectRef: owner, Status: "ACTIVE", ValidUntil: h.now.Add(-time.Hour)},
		"c-revoked": {ID: "c-revoked", SubjectRef: owner, Status: "REVOKED", ValidUntil: h.now.Add(time.Hour)},
	}}
	h.portal.Consents = register
	return h, register
}

func TestRevokeAnActiveConsent(t *testing.T) {
	h, register := revokeHarness(t)

	status, raw := h.call(t, http.MethodDelete, "/portal/consents/c-active", citizenA, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, raw)
	}
	if len(register.revoked) != 1 || register.recs["c-active"].Status != "REVOKED" {
		t.Fatalf("revoked = %v, record %+v", register.revoked, register.recs["c-active"])
	}
}

// An expired or revoked consent is refused with CONSENT_NOT_REVOCABLE, and
// the register is not asked to revoke it (#600).
func TestRevokeRefusesAConsentThatIsNotActive(t *testing.T) {
	for _, id := range []string{"c-expired", "c-revoked"} {
		t.Run(id, func(t *testing.T) {
			h, register := revokeHarness(t)
			before := register.recs[id]

			status, raw := h.call(t, http.MethodDelete, "/portal/consents/"+id, citizenA, nil)
			if status != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body %s", status, raw)
			}
			if got := decode(t, raw)["reason"]; got != "CONSENT_NOT_REVOCABLE" {
				t.Errorf("reason = %v, want CONSENT_NOT_REVOCABLE", got)
			}
			if len(register.revoked) != 0 || register.recs[id].Status != before.Status {
				t.Errorf("the consent changed: revoked %v, record %+v", register.revoked, register.recs[id])
			}
		})
	}
}

// Another citizen is refused as before, whatever the consent's status.
func TestRevokeOfAnotherCitizensConsentIsForbidden(t *testing.T) {
	for _, id := range []string{"c-active", "c-expired", "c-revoked"} {
		t.Run(id, func(t *testing.T) {
			h, register := revokeHarness(t)

			status, raw := h.call(t, http.MethodDelete, "/portal/consents/"+id, citizenB, nil)
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %s", status, raw)
			}
			if got := decode(t, raw)["reason"]; got != "consent_not_owned_by_caller" {
				t.Errorf("reason = %v, want consent_not_owned_by_caller", got)
			}
			if len(register.revoked) != 0 {
				t.Errorf("revoked = %v, want none", register.revoked)
			}
		})
	}
}
