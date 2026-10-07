package dvtp.gbo.fixtures_test

# Test input: a GraphQL request as the Inway hands it to the PDP, with the
# field list the mapper adds (Section 6.2) for the demo consumers' queries.

import data.dvtp.gbo.graphql_schemas

hv := "99999999900000000300"

eudi_issuer := "99999999900000000100"

# ── The request ──────────────────────────────────────────────────────────

graphql(service, fields) := {
	"profile": "ftv-graphql/0.1",
	"operation": {"type": "query", "name": null},
	"schema": {"digest": graphql_schemas.digests[service]},
	"fields": fields,
	"unverifiable": null,
}

request(actor, service, fields, headers) := request_with(actor, service, graphql(service, fields), headers)

# A request with mapper output of the test's own making.
request_with(actor, service, output, headers) := {
	"subject": {"type": "identity", "id": actor, "attributes": {"service_name": service}},
	"action": {"id": "POST", "type": "name"},
	"resource": {"type": "uri", "id": "/graphql", "attributes": {"graphql": output}},
	"context": {"time": "2026-07-06T12:00:00Z", "headers": headers},
}

# Headers without a consent token; tests that need a consent stand one in.
scope_headers(scope) := {"Content-Type": "application/json", "X-Gbo-Scope": scope}

# ── Arguments ────────────────────────────────────────────────────────────

literal(value) := {"value": value, "origin": "literal"}

variable(name, value) := {"value": value, "origin": sprintf("variable:%s", [name]), "variables": [name]}

schema_default(value) := {"value": value, "origin": "schema-default"}

# ── BD income ────────────────────────────────────────────────────────────
#   query($bsn: BSN!) { ingeschrevenPersoon(bsn: $bsn) {
#     heeftBelastingjaarAangifte(belastingjaren: <years>) {
#       belastingjaar ... on AangifteIH { box1Inkomen { waarde } } } } }

person(bsn) := person_as(["ingeschrevenPersoon"], variable("bsn", bsn))

person_as(path, bsn_arg) := {
	"path": path,
	"parentType": "Query",
	"field": "ingeschrevenPersoon",
	"leaf": false,
	"args": {"bsn": bsn_arg},
}

declarations(years_arg) := object.union(declarations_without_years, {"args": {"belastingjaren": years_arg}})

declarations_without_years := {
	"path": ["ingeschrevenPersoon", "heeftBelastingjaarAangifte"],
	"parentType": "IngeschrevenPersoon",
	"field": "heeftBelastingjaarAangifte",
	"leaf": false,
}

year := {
	"path": ["ingeschrevenPersoon", "heeftBelastingjaarAangifte", "belastingjaar"],
	"parentType": "BelastingjaarAangifte",
	"field": "belastingjaar",
	"leaf": true,
}

box(name) := {
	"path": ["ingeschrevenPersoon", "heeftBelastingjaarAangifte", name],
	"parentType": "AangifteIH",
	"on": "AangifteIH",
	"field": name,
	"leaf": false,
}

amount(name) := {
	"path": ["ingeschrevenPersoon", "heeftBelastingjaarAangifte", name, "waarde"],
	"parentType": "Bedrag",
	"field": "waarde",
	"leaf": true,
}

income_fields(bsn, years_arg) := [person(bsn), declarations(years_arg), year, box("box1Inkomen"), amount("box1Inkomen")]

income(actor, bsn, years, headers) := request(actor, "bri", income_fields(bsn, literal(years)), headers)

# ── BRP death certificate ────────────────────────────────────────────────
#   query($bsn: BSN!) { akteVanOverlijden(bsn: $bsn) { verklaring_tekst } }

death_certificate_fields(bsn) := [
	{"path": ["akteVanOverlijden"], "parentType": "Query", "field": "akteVanOverlijden", "leaf": false, "args": {"bsn": variable("bsn", bsn)}},
	{"path": ["akteVanOverlijden", "verklaring_tekst"], "parentType": "AkteVanOverlijden", "field": "verklaring_tekst", "leaf": true},
]

# ── LVG ownership ────────────────────────────────────────────────────────
#   query($bsn: BSN!, $vboId: String!) { vbo(bsn: $bsn, vboId: $vboId) { vboId } }

vbo(bsn) := {
	"path": ["vbo"],
	"parentType": "Query",
	"field": "vbo",
	"leaf": false,
	"args": {"bsn": variable("bsn", bsn), "vboId": variable("vboId", "0632010000099412")},
}

ownership_fields(bsn) := [
	vbo(bsn),
	{"path": ["vbo", "vboId"], "parentType": "Verblijfsobject", "field": "vboId", "leaf": true},
]

# ── Reading a denied response (profile Section 10.1) ─────────────────────

client(result) := result.context.graphql.client

admin(result) := result.context.graphql.admin

request_code(result) := object.filter(admin(result), {"code", "subcode"})

denied(result) := object.get(admin(result), "denied_fields", [])

field_codes(result) := {d.code | some d in denied(result)}

# The consumer is told a field was refused, and some field was, with code.
refused_for(result, code) if {
	result.decision == false
	client(result) == {"code": "FIELD_NOT_PERMITTED"}
	code in field_codes(result)
}
