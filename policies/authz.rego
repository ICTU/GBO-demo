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
# context.trace_id. OpenFTV injects context.time.

import data.dvtp.gbo

default allow := false

# Source-metadata is transported over its own FSC service and carries no
# GraphQL body or citizen identifier. Subject, method and endpoint are each
# pinned exactly, so all other non-GraphQL traffic stays fail-closed.
#
# What this rule can no longer tell is WHICH FSC service the request arrived
# on. The PDP sees the caller's OIN, the method and the path; it does not see
# the service name (the request-mapper reads fsc-authorization, the
# transaction id, the scope header and the consent token — no service). The
# flow property stood in for that, so dropping it widens this rule by exactly
# one case: one of the two OINs below, arriving on a contract other than the
# metadata one, issuing GET /.well-known/gbo.
#
# That is judged acceptable rather than harmless. FSC routes each service to
# its own upstream (gbo-metadata-bd -> graphql-server:4000, the bri data
# service -> the sidecar), and the document is a public description of a
# service, carrying no citizen data. So the widening admits the same peer to
# the same class of document — not a new principal, and not a new kind of
# content. If the service name is ever surfaced to the PDP, gate on that and
# the rule becomes exact again without reinstating a declared property.
_source_metadata_request if {
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
