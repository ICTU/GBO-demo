package authz

# Entry-point evaluated by the OpenFTV PDP at path /authz. OpenFTV reads two
# keys from this package and nothing else: `allow` (bool) gates the
# decision, and on DENY `reason` (string) becomes context.reason_user.en in
# the AuthZEN response.
#
# That response is what OpenFTV's Authorization Decision Log (ADL) records,
# and the ADL is the authoritative audit record of an authorization
# decision. So `reason` is the part of the decision detail that reaches the
# audit record: the reason code of a denial. The FSC Inway also returns
# reason_user to the caller in its 401, which is why `reason` is a code and
# never carries data.
#
# `response` is the fuller document: granted[] and denied_fields[] per
# field, with the deciding rule and its evaluation steps. OpenFTV does not
# transport it. It appears only in the embedded OPA's console decision log,
# which the developer portal reads from Loki as observability. It is not a
# record of the decision, and nothing may depend on it as one.
#
# Input shape (OpenFTV AuthZEN mapping): {subject, action, resource,
# context}. The FTV GraphQL mapper inside this image adds the field list of
# a GraphQL request as input.resource.attributes.graphql, validated against
# the schema of the FSC service in input.subject.attributes.service_name.
# OpenFTV injects context.time. The consent is not in input:
# data.dvtp.gbo.consent resolves it from the token in context.headers while
# the policy evaluates.

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

reason := response.context.reason_admin.code if {
	not response.decision
}
