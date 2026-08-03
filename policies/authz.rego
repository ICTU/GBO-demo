package authz

# Entry-point evaluated by the OpenFTV PDP at path /authz. The OpenFTV
# contract: `allow` (bool) gates the decision; `reason` (string) is
# surfaced on DENY as context.reasonUser.en in the AuthZEN response.
#
# OpenFTV evaluates the whole package document, so `response` is part of
# the decision result and lands in the Decision Log — the dev-portal
# reads granted[]/denied_fields[]/steps from there.
#
# Input shape (OpenFTV AuthZEN mapping): {subject, action, resource,
# context}. The GraphQL request-mapper inside the PDP image places its
# enrichment under input.context: context.resolved (GraphQL fields),
# context.resource (scope/query/variables), context.flow, context.pip.pid
# (EUDI pseudonym), context.trace_id. OpenFTV injects context.time.
# Consent is not handed in — consent.rego retrieves it during evaluation.

import data.dvtp.gbo

default allow := false

allow if gbo.response.decision

reason := gbo.response.context.reason_admin.code if {
	not gbo.response.decision
}

response := gbo.response
