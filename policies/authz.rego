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
# context}. The pdp-service context-handler places its enrichment under
# input.context: context.resolved (GraphQL fields), context.pip
# (consent/PID), context.resource (scope/query/variables/pi),
# context.trace_id. OpenFTV injects context.time.

import data.dvtp.gbo

default allow := false

allow if gbo.response.decision

reason := gbo.response.context.reason_admin.code if {
	not gbo.response.decision
}

response := gbo.response
