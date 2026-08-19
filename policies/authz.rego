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
# context}. The GraphQL request-mapper inside this image places its
# enrichment under input.context: context.resolved (GraphQL fields),
# context.pip (consent/PID), context.resource (scope/query/variables/pi),
# context.flow, context.trace_id. OpenFTV injects context.time.

import data.dvtp.gbo

default allow := false

# Source-metadata is transported over its own FSC service and carries no
# GraphQL body or citizen identifier. The provider-owned additional claim in
# the FSC token selects this narrow policy path. Only the GBO consumer peer may
# use it; method and endpoint remain exact so all other non-GraphQL traffic
# stays fail-closed.
_source_metadata_request if {
	input.context.flow == "gbo:source-metadata"
	input.subject.id in {
		"99999999900000000100", # local Docker Compose
		"0000009961MINEZK0000", # simulation MinEZK
	}
	input.action.id == "GET"
	input.resource.id == "/.well-known/gbo"
}

response := {
	"decision": true,
	"context": {"granted": [{"rule": "SOURCE_METADATA_FSC"}]},
} if {
	_source_metadata_request
}

else := gbo.response

allow if response.decision

reason := response.context.reason_admin.code if {
	not response.decision
}
