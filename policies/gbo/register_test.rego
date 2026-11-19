package gbo.register_test

import data.gbo.register

# The register's policy feed, as GET /internal/openftv/participants returns it.
_feed := [
	{
		"peer_id": "99999999900000000300",
		"name": "Demo Hypotheekverlener BV",
		"active": true,
		"allowed_source_peer_ids": ["99999999900000000200"],
		"acts_for": [],
	},
	{
		"peer_id": "99999999900000001100",
		"name": "Demo Integrator BV",
		"active": false,
		"allowed_source_peer_ids": [],
		"acts_for": [{"peer_id": "99999999900000000300", "rules": ["DVT0001"]}],
	},
]

_ok(_) := {"status_code": 200, "body": _feed}

_empty(_) := {"status_code": 200, "body": []}

_server_error(_) := {"status_code": 500, "body": {"error": "register unavailable"}}

_not_a_list(_) := {"status_code": 200, "body": {"peer_id": "99999999900000000300"}}

_network_error(_) := {"status_code": 0, "error": {"code": "eval_http_send_network_error", "message": "dial tcp: connection refused"}}

test_participants_are_keyed_by_peer_id if {
	p := register.participants with http.send as _ok
	object.keys(p) == {"99999999900000000300", "99999999900000001100"}
	p["99999999900000001100"].active == false
	p["99999999900000001100"].acts_for[0].rules == ["DVT0001"]
}

test_an_empty_register_is_available if {
	register.participants == {} with http.send as _empty
}

test_a_server_error_is_unavailable if {
	register.participants == null with http.send as _server_error
}

test_a_network_error_is_unavailable if {
	register.participants == null with http.send as _network_error
}

test_a_body_that_is_no_list_is_unavailable if {
	register.participants == null with http.send as _not_a_list
}

test_an_entry_without_peer_id_is_skipped if {
	feed := array.concat(_feed, [{"name": "no id"}])
	register.participants == {p.peer_id: p | some p in _feed} with http.send as {"status_code": 200, "body": feed}
}

# The URL comes from the PDP's environment, with the demo's as default.
test_url_from_the_environment if {
	register.url == "http://register.example/participants" with opa.runtime as {"env": {"GBO_REGISTER_URL": "http://register.example/participants"}}
}

test_url_default if {
	register.url == "http://dvtp-onboarding-register:4015/internal/openftv/participants" with opa.runtime as {"env": {}}
}
