package main

import "testing"

// The wire format the FSC Inway actually produces: the policy's reason code
// embedded in prose. Pinning it here documents what the backend parses; the
// citizen-facing decision is asserted on DenialCode, never on this text.
func inwayMessage(code string) string {
	return "authorization server denied request: reasonUser-en: " + code + "; "
}

func TestDenialCodeForDisclosesOnlyCitizenActionableCodes(t *testing.T) {
	tests := map[string]struct {
		reason string
		want   string
	}{
		// Disclosable: the citizen granted this consent and can grant it again.
		"revoked consent": {inwayMessage("CONSENT_WITHDRAWN"), "CONSENT_WITHDRAWN"},
		"expired consent": {inwayMessage("CONSENT_EXPIRED"), "CONSENT_EXPIRED"},

		// Administrative: real codes, but they describe how the policy is
		// built rather than anything the citizen did or can undo.
		"actor not allowed":   {inwayMessage("ACTOR_NOT_ALLOWED"), DenialCodeUnavailable},
		"constraint mismatch": {inwayMessage("CONSTRAINT_MISMATCH"), DenialCodeUnavailable},
		"no applicable rule":  {inwayMessage("NO_APPLICABLE_RULE"), DenialCodeUnavailable},
		"scope mismatch":      {inwayMessage("CONSENT_SCOPE_MISMATCH"), DenialCodeUnavailable},
		"signature invalid":   {inwayMessage("CONSENT_SIGNATURE_INVALID"), DenialCodeUnavailable},
		"status unavailable":  {inwayMessage("CONSENT_STATUS_UNAVAILABLE"), DenialCodeUnavailable},

		// A code this backend does not know has not been judged safe by
		// anyone, so it must not be forwarded on optimism.
		"unknown code": {inwayMessage("SOME_FUTURE_CODE"), DenialCodeUnavailable},

		// Transport failures carry no code at all.
		"bare status":     {"upstream_error: status 401", DenialCodeUnavailable},
		"outway down":     {"fsc_outway_call_failed: dial tcp: connection refused", DenialCodeUnavailable},
		"empty reason":    {"", DenialCodeUnavailable},
		"inway no reason": {"authorization server denied request: ", DenialCodeUnavailable},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := denialCodeFor(tc.reason); got != tc.want {
				t.Fatalf("denialCodeFor(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// The reason text is not a stable contract, so the code is recovered by
// looking for known codes rather than by matching a phrasing.
func TestPolicyCodeFromToleratesPhrasing(t *testing.T) {
	tests := map[string]struct {
		reason string
		want   string
	}{
		"inway prose":     {inwayMessage("CONSENT_WITHDRAWN"), "CONSENT_WITHDRAWN"},
		"bare code":       {"CONSENT_WITHDRAWN", "CONSENT_WITHDRAWN"},
		"legacy prefix":   {"denied by policy: CONSENT_WITHDRAWN", "CONSENT_WITHDRAWN"},
		"no code present": {"upstream_error: status 401", ""},
		"unknown token":   {"denied by policy: SOME_FUTURE_CODE", ""},

		// Several codes in one message: the cascade's winner must not be
		// demoted by a lower-priority code that happens to appear too.
		"priority wins": {
			"reasonUser-en: CONSENT_SIGNATURE_INVALID; steps: CONSENT_WITHDRAWN, NO_APPLICABLE_RULE",
			"CONSENT_SIGNATURE_INVALID",
		},
		"priority wins reversed": {
			"steps: NO_APPLICABLE_RULE, CONSENT_WITHDRAWN; reasonUser-en: CONSENT_SIGNATURE_INVALID",
			"CONSENT_SIGNATURE_INVALID",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := policyCodeFrom(tc.reason); got != tc.want {
				t.Fatalf("policyCodeFrom(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// A disclosable code must also be a code the policy can actually emit;
// otherwise the allow-list is describing something that no longer exists.
func TestDisclosableCodesAreRealPolicyCodes(t *testing.T) {
	for code := range disclosableDenyCodes {
		if !policyDenyCodes[code] {
			t.Fatalf("%s is disclosable but not a known policy code", code)
		}
	}
}
