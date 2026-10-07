package authz

# Entry point evaluated by the OpenFTV PDP at /authz.
#
# Input follows the OpenFTV AuthZEN mapping: {subject, action, resource,
# context}. For a GraphQL request the FTV mapper adds the requested fields as
# input.resource.attributes.graphql. The consent is not in input:
# data.dvtp.gbo.consent resolves it from the token in context.headers.

import data.dvtp.gbo

default allow := false

# The source-metadata document: public, without GraphQL body or citizen data.
# Allowed only for two known peers, on one of the metadata services, on
# exactly GET /.well-known/gbo. The FSC service is the signed service_name
# of the access token. The same path on a data service is judged like any
# other request: this exception does not skip the GraphQL checks there.
_metadata_services := {"gbo-metadata-bd", "gbo-metadata-rvig"}

_source_metadata_request if {
	input.subject.id in {
		"99999999900000000100", # local Docker Compose
		"0000009961MINEZK0000", # simulation MinEZK
	}
	input.subject.attributes.service_name in _metadata_services
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

# OpenFTV transports only `allow` and `reason`. On a deny, `reason` becomes
# reason_user.en, which the authoritative decision log (ADL) records and the
# FSC Inway returns to the caller: the consumer's code (graphql.client) and,
# for an unverifiable request, its subcode, e.g. "COVERAGE_UNVERIFIABLE
# PARSE_ERROR". Never request data. The rest of `response` reaches only the
# OPA console decision log, which the developer portal reads.
reason := concat(" ", [part | some part in [client.code, object.get(client, "subcode", "")]; part != ""]) if {
	not response.decision
	client := response.context.graphql.client
}
