package consumer

import "testing"

// inwayMessage is the reason text the FSC Inway sends.
func inwayMessage(code string) string {
	return "authorization server denied request: reasonUser-en: " + code + "; "
}

func TestDenialCodeForDisclosesOnlyCitizenActionableCodes(t *testing.T) {
	tests := map[string]struct {
		reason string
		want   string
	}{
		"refused field": {inwayMessage("FIELD_NOT_PERMITTED"), "FIELD_NOT_PERMITTED"},

		"access denied":         {inwayMessage("ACCESS_DENIED"), DenialCodeUnavailable},
		"unverifiable request":  {inwayMessage("COVERAGE_UNVERIFIABLE PARSE_ERROR"), DenialCodeUnavailable},
		"operation unsupported": {inwayMessage("OPERATION_NOT_SUPPORTED"), DenialCodeUnavailable},
		"no data fields":        {inwayMessage("NO_DATA_FIELDS"), DenialCodeUnavailable},

		// The policy no longer tells a consumer why a field was refused.
		"old consent code": {inwayMessage("CONSENT_WITHDRAWN"), DenialCodeUnavailable},
		"unknown code":     {inwayMessage("SOME_FUTURE_CODE"), DenialCodeUnavailable},

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

func TestPolicyCodeFromToleratesPhrasing(t *testing.T) {
	tests := map[string]struct {
		reason string
		want   string
	}{
		"inway prose":     {inwayMessage("FIELD_NOT_PERMITTED"), "FIELD_NOT_PERMITTED"},
		"bare code":       {"FIELD_NOT_PERMITTED", "FIELD_NOT_PERMITTED"},
		"legacy prefix":   {"denied by policy: ACCESS_DENIED", "ACCESS_DENIED"},
		"with subcode":    {inwayMessage("COVERAGE_UNVERIFIABLE PARSE_ERROR"), "COVERAGE_UNVERIFIABLE"},
		"no code present": {"upstream_error: status 401", ""},
		"unknown token":   {"denied by policy: SOME_FUTURE_CODE", ""},
		"old code":        {"denied by policy: CONSENT_WITHDRAWN", ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := PolicyCode(tc.reason); got != tc.want {
				t.Fatalf("PolicyCode(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

// The allow-list may only name codes the policy emits.
func TestDisclosableCodesAreRealPolicyCodes(t *testing.T) {
	for code := range disclosableDenyCodes {
		if !policyDenyCodes[code] {
			t.Fatalf("%s is disclosable but not a known policy code", code)
		}
	}
}
