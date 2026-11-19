package gbo.register

# Admission PIP: the DvTP onboarding register's participants, asked while the
# policy evaluates.
#
# Read with http.send on every evaluation that needs it, like the consent
# status (consent.rego), rather than pulled by OpenFTV on an interval. A
# change in the register then counts from the next decision, and a PDP that
# has just started decides on the register at once: there is no window in
# which it answers before its first pull has arrived.
#
# Two policies read it: contract auto-signing (policies/fsc/autosign.rego),
# which admits the parties on an FSC contract, and the engine, which checks
# an integrator's mandate on every delegated call.
#
# Each participant: peer_id, name, active, allowed_source_peer_ids and, for
# an integrator, acts_for (the service providers and rules it acts for).

_timeout := "2s"

_env := object.get(opa.runtime(), "env", {})

# From the PDP's environment, defaulting to the demo deployment.
url := value if {
	value := _env.GBO_REGISTER_URL
	is_string(value)
	value != ""
} else := "http://dvtp-onboarding-register:4015/internal/openftv/participants"

_response := http.send({
	"method": "GET",
	"url": url,
	"timeout": _timeout,
	"raise_error": false,
})

# The participants by Peer ID, or null when the register did not answer with
# a list. An empty register is a register: it admits nobody. Only null means
# the register is unavailable.
participants := {p.peer_id: p |
	some p in _response.body
	is_string(p.peer_id)
} if {
	_response.status_code == 200
	is_array(_response.body)
} else := null
