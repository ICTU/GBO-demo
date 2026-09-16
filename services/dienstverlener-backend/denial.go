package main

import "regexp"

// ── The disclosure boundary for a denial ─────────────────────────────────
//
// A denial reaches this backend as free text. The FSC Inway answers 401
// with an RFC9457-ish body whose `message` carries the policy's reason
// code embedded in prose ("authorization server denied request:
// reasonUser-en: CONSENT_WITHDRAWN; "), and older or other upstreams may
// answer with a bare status and no reason at all.
//
// This file is where that prose stops. The backend decides what a citizen
// may be told; the UI only renders what it is given, and switches on
// DenialCode — never on Reason, which stays a technical string for logs
// and for an operator-facing footer.
//
// Two rules follow from that split:
//
//   - Only codes a citizen can act on are passed through. A revoked or an
//     expired consent is something they granted and can grant again.
//     Everything else — ACTOR_NOT_ALLOWED, CONSTRAINT_MISMATCH,
//     NO_APPLICABLE_RULE — tells a citizen nothing and discloses how the
//     policy is structured, so it collapses to one honest message.
//
//   - An unrecognised code is not disclosable either. A code this backend
//     does not know is a code whose citizen-safety nobody has judged, so
//     it collapses too rather than being forwarded on the assumption that
//     a new code is harmless.

// DenialCodeUnavailable is the catch-all: we could not retrieve the data,
// and the reason is not the citizen's to act on. Every denial that is not
// explicitly disclosable becomes this, including transport failures, so
// the UI has exactly one field to switch on.
const DenialCodeUnavailable = "UNAVAILABLE"

// policyDenyCodes are the reason codes the GBO policy can produce, per the
// priority cascade in policies/dvtp/gbo/engine.rego. Membership here means
// "this backend recognises the code", not "this code may be shown".
//
// Keep in step with engine.rego: a code the policy emits but this set omits
// degrades to DenialCodeUnavailable, which is safe but loses a message the
// citizen could have acted on.
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

// disclosableDenyCodes is the subset a citizen may be shown. Both describe
// a consent the citizen gave and can give again, so naming them is both
// actionable and safe. Adding to this set is a disclosure decision.
var disclosableDenyCodes = map[string]bool{
	"CONSENT_WITHDRAWN": true,
	"CONSENT_EXPIRED":   true,
}

// upstreamCodeToken matches a SCREAMING_SNAKE_CASE token. The reason text
// is not a stable contract — it has already changed shape once upstream —
// so the code is recovered by looking for known codes anywhere in it
// rather than by parsing a particular phrasing.
var upstreamCodeToken = regexp.MustCompile(`[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+`)

// policyCodeFrom recovers the policy's reason code from an upstream denial
// message, or "" when the message carries none this backend recognises.
//
// The highest-priority code wins when several appear, matching how
// engine.rego picks the reason it reports: a message that happens to
// mention a second code cannot demote the real one.
func policyCodeFrom(reason string) string {
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

// denyCodePriority mirrors _code_priority in policies/dvtp/gbo/engine.rego.
// Only the ordering matters here, not the absolute values.
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

// denialCodeFor turns an upstream denial message into the code the UI may
// render. Everything that is not explicitly disclosable — an
// administrative denial, an unrecognised code, a transport failure with no
// code at all — becomes DenialCodeUnavailable.
func denialCodeFor(reason string) string {
	if code := policyCodeFrom(reason); disclosableDenyCodes[code] {
		return code
	}
	return DenialCodeUnavailable
}
