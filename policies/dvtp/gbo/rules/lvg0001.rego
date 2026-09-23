package dvtp.gbo.rules.lvg0001

# LVG0001 — Ownership check of a verblijfsobject via consent.
#
# The LVG/Installatie Register pilot: the citizen consents in MijnOverheid
# that the Installatie Register may ask LVG whether a verblijfsobject is
# theirs. The query names the citizen (as a PI from that consent) and one
# VBO-id; LVG answers with that same VBO-id or null. The Installatie
# Register's own consent for the installer is not a GBO consent and is not
# checked here.
#
# The same consent checks as DVT0001, with the scope pinned to the one this
# rule exists for: without allowed_scopes a token for bd:ib:2025, sent with
# that scope, would pass the consent-scope axis on LVG fields.

rule_id := "LVG0001"

covers_types := set()

covers_fields := {
	# object-edge (parent-traversal requires it)
	"Query.vbo",
	# the only answer: the requested VBO-id, when the citizen owns it
	"Verblijfsobject.vboId",
}

spec := {
	"rule_id": "LVG0001",
	"consent_required": true,
	"consent_context_required": true,
	"consent_status_required": true,
	"consent_actor_binding": true,
	"consent_must_cover_scope": true,
	"allowed_scopes": {"lvg:vbo:eigendom"},
	# The PI in the query must be the PI of the verified consent, so the
	# Installatie Register can only ask about the citizen who consented.
	"constraint_binding": [{
		"arg": "bsn",
		"resource_field": "pi",
	}],
	"years_in_scopes": false,
	"pip": null,
}
