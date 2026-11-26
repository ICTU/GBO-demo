package portalhttp

import (
	"net/http"
	"testing"
)

// A grant without the citizen's confirmation is refused with
// CONFIRMATION_MISSING on both ways in: the decision on a pushed request and
// a direct POST /portal/consents (issue #598).

func TestDecisionWithoutConfirmationIsRefused(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"confirmation left out": {"decision": "grant", "scopes": []string{"bd:ib:2025"}},
		"confirmation false":    {"decision": "grant", "scopes": []string{"bd:ib:2025"}, "information_confirmed": false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, returnURL)
			uri := h.push(t, returnURL)
			h.resolve(t, uri, hypotheekPeer, citizenA)

			status, raw := h.decide(t, uri, citizenA, body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", status, raw)
			}
			if got := decode(t, raw)["reason"]; got != "CONFIRMATION_MISSING" {
				t.Errorf("reason = %v, want CONFIRMATION_MISSING (%s)", got, raw)
			}
			if h.register.consents() != 0 {
				t.Error("an unconfirmed grant gave a consent")
			}
			if status, body := h.resolve(t, uri, hypotheekPeer, citizenA); status != http.StatusOK {
				t.Errorf("the reference is no longer usable: %d %v", status, body)
			}
		})
	}
}

func TestGiveConsentWithoutConfirmationIsRefused(t *testing.T) {
	h := newHarness(t, returnURL)

	status, raw := h.call(t, http.MethodPost, "/portal/consents", citizenA, map[string]any{
		"dienstverlener_oin": hypotheekPeer,
		"scopes":             []string{"bd:ib:2025"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", status, raw)
	}
	if got := decode(t, raw)["reason"]; got != "CONFIRMATION_MISSING" {
		t.Errorf("reason = %v, want CONFIRMATION_MISSING (%s)", got, raw)
	}
	if h.register.consents() != 0 {
		t.Error("an unconfirmed grant gave a consent")
	}
}
