package doelbinding.readiness_test

import data.doelbinding.readiness

test_not_ready_before_the_first_pip_pull if {
	not readiness.allow with data.entities as {}
}

test_ready_once_the_register_is_pulled if {
	readiness.allow with data.entities as {"dvtp_participant": {"0000009950HYPBV00000": {
		"name": "Demo Hypotheekverlener BV",
		"active": true,
		"allowed_source_peer_ids": ["0000009958MINBZK0000"],
		"acts_for": [],
	}}}
}

test_ready_with_an_empty_register if {
	readiness.allow with data.entities as {"dvtp_participant": {}}
}
