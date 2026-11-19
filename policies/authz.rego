package authz

# Entry point evaluated by the OpenFTV PDP at /authz.
#
# Input follows the OpenFTV AuthZEN mapping: {subject, action, resource,
# context}. For a GraphQL request the FTV mapper adds the requested fields as
# input.resource.attributes.graphql. The consent is not in input:
# data.gbo.consent resolves it from the token in context.headers.

import data.gbo

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

# LDV_READ_FSC (#686): the consent register reads the logbook of a source's
# delivery logbook through the read extension, over FSC. Only the register's
# peer, only on a read service of a component, only
# POST /data-processing-operations, and only with a body that holds exactly
# one 32-hex traceId and nothing else. A read by data subject, activity or
# time window is refused even though the read extension allows it: the
# register knows which trace it wants from the data usage index, and
# needs nothing else. It sits before the GraphQL checks because the body is
# not GraphQL.
_ldv_read_services := {"ldv-lezen-bd", "ldv-lezen-brp", "ldv-lezen-lvg"}

_consent_register_peer := "99999999900000000700"

_ldv_read_service if input.subject.attributes.service_name in _ldv_read_services

_ldv_read_request if {
	_ldv_read_service
	input.subject.id == _consent_register_peer
	input.action.id == "POST"
	input.resource.id == "/data-processing-operations"
	body := json.unmarshal(input.action.attributes.body)
	is_object(body)
	object.keys(body) == {"traceId"}
	is_string(body.traceId)
	regex.match(`^[0-9a-f]{32}$`, body.traceId)
}

response := {
	"decision": true,
	"context": {"granted": [{"rule": "SOURCE_METADATA_FSC"}]},
} if {
	_source_metadata_request
}

else := {
	"decision": true,
	"context": {"granted": [{"rule": "LDV_READ_FSC"}]},
} if {
	_ldv_read_request
}

# A read service is never judged as GraphQL: what the rule above does not
# allow is refused here.
else := {
	"decision": false,
	"context": {"reason_admin": {"code": "LDV_READ_NOT_PERMITTED"}},
} if {
	_ldv_read_service
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

# A refused read of a component logbook carries its own bare code.
reason := "LDV_READ_NOT_PERMITTED" if {
	not response.decision
	_ldv_read_service
}
