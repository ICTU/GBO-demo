package consumer

import "regexp"

// This file decides what a citizen may be told about a denied query. The FSC
// Inway embeds the policy's reason code in the prose of `message`; the UI
// renders only the DenialCode derived from it. Codes a citizen can act on pass
// through; everything else, including codes not listed here, becomes
// DenialCodeUnavailable.

// DenialCodeUnavailable covers every denial that is not disclosable,
// transport failures included.
const DenialCodeUnavailable = "UNAVAILABLE"

// policyDenyCodes mirrors the codes in policies/dvtp/gbo/engine.rego. A code
// missing here degrades safely to DenialCodeUnavailable.
var policyDenyCodes = map[string]bool{
	"ACTOR_NOT_ALLOWED":          true,
	"CONSENT_ACTOR_MISMATCH":     true,
	"CONSENT_CONTEXT_INVALID":    true,
	"CONSENT_EXPIRED":            true,
	"CONSENT_KEYS_UNAVAILABLE":   true,
	"CONSENT_NOT_FOUND":          true,
	"CONSENT_SCOPE_MISMATCH":     true,
	"CONSENT_SIGNATURE_INVALID":  true,
	"CONSENT_STATUS_UNAVAILABLE": true,
	"CONSENT_TOKEN_EXPIRED":      true,
	"CONSENT_WITHDRAWN":          true,
	"CONSTRAINT_MISMATCH":        true,
	"COVERAGE_UNVERIFIABLE":      true,
	"NO_APPLICABLE_RULE":         true,
	"PID_NOT_PRESENT":            true,
	"SCOPE_NOT_ALLOWED":          true,
	"UNKNOWN":                    true,
	"YEAR_NOT_ALLOWED":           true,
	"YEAR_NOT_COVERED":           true,
}

// disclosableDenyCodes may be shown to a citizen: both concern a consent they
// can grant again.
var disclosableDenyCodes = map[string]bool{
	"CONSENT_WITHDRAWN": true,
	"CONSENT_EXPIRED":   true,
}

// upstreamCodeToken finds codes anywhere in the text, because the phrasing
// around them is not a contract.
var upstreamCodeToken = regexp.MustCompile(`[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+`)

// PolicyCode returns the known code in reason, or "". When several
// appear, the engine.rego priority decides.
func PolicyCode(reason string) string {
	best := ""
	for _, token := range upstreamCodeToken.FindAllString(reason, -1) {
		if !policyDenyCodes[token] {
			continue
		}
		if best == "" || denyCodePriority(token) > denyCodePriority(best) {
			best = token
		}
	}
	return best
}

// denyCodePriority mirrors _code_priority in engine.rego; only the order matters.
func denyCodePriority(code string) int {
	switch code {
	case "CONSENT_SIGNATURE_INVALID":
		return 73
	case "CONSENT_TOKEN_EXPIRED":
		return 72
	case "CONSENT_KEYS_UNAVAILABLE":
		return 71
	case "CONSENT_CONTEXT_INVALID":
		return 70
	case "CONSENT_STATUS_UNAVAILABLE":
		return 69
	case "CONSENT_ACTOR_MISMATCH":
		return 68
	case "ACTOR_NOT_ALLOWED":
		return 65
	case "YEAR_NOT_ALLOWED":
		return 63
	case "SCOPE_NOT_ALLOWED":
		return 62
	case "CONSENT_NOT_FOUND":
		return 60
	case "PID_NOT_PRESENT":
		return 55
	case "CONSENT_WITHDRAWN":
		return 50
	case "CONSENT_EXPIRED":
		return 45
	case "YEAR_NOT_COVERED":
		return 41
	case "CONSENT_SCOPE_MISMATCH":
		return 40
	case "CONSTRAINT_MISMATCH":
		return 30
	case "NO_APPLICABLE_RULE":
		return 25
	default:
		return 5
	}
}

// denialCodeFor returns the code the UI may render for reason.
func denialCodeFor(reason string) string {
	if code := PolicyCode(reason); disclosableDenyCodes[code] {
		return code
	}
	return DenialCodeUnavailable
}
