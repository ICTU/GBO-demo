package consumer

import "regexp"

// This file decides what a citizen may be told about a denied query. The FSC
// Inway embeds the policy's reason in the prose of `message`: one of the FTV
// GraphQL profile's codes for the consumer, sometimes followed by a subcode.
// The UI renders only the DenialCode derived from it. A refusal passes
// through, so the citizen can be offered to consent again; everything else,
// including codes not listed here, becomes DenialCodeUnavailable.

// DenialCodeUnavailable covers every denial that is not disclosable,
// transport failures included.
const DenialCodeUnavailable = "UNAVAILABLE"

// policyDenyCodes are the codes the policy gives a consumer (FTV GraphQL
// profile, Section 10.2). They say that the request was refused, never why
// or which field: that detail stays with the PDP. A code missing here
// degrades safely to DenialCodeUnavailable.
var policyDenyCodes = map[string]bool{
	"ACCESS_DENIED":           true,
	"COVERAGE_UNVERIFIABLE":   true,
	"FIELD_NOT_PERMITTED":     true,
	"NO_DATA_FIELDS":          true,
	"OPERATION_NOT_SUPPORTED": true,
}

// disclosableDenyCodes may be shown to a citizen. A refused field is the one
// a citizen can act on, by consenting again; the others are failures on our
// or the consumer's side.
var disclosableDenyCodes = map[string]bool{
	"FIELD_NOT_PERMITTED": true,
}

// upstreamCodeToken finds codes anywhere in the text, because the phrasing
// around them is not a contract.
var upstreamCodeToken = regexp.MustCompile(`[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+`)

// PolicyCode returns the first known code in reason, or "". A subcode that
// follows it (COVERAGE_UNVERIFIABLE PARSE_ERROR) is not a code of its own.
func PolicyCode(reason string) string {
	for _, token := range upstreamCodeToken.FindAllString(reason, -1) {
		if policyDenyCodes[token] {
			return token
		}
	}
	return ""
}

// denialCodeFor returns the code the UI may render for reason.
func denialCodeFor(reason string) string {
	if code := PolicyCode(reason); disclosableDenyCodes[code] {
		return code
	}
	return DenialCodeUnavailable
}
