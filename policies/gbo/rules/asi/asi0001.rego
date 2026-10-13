package gbo.rules.asi.asi0001

# ASI0001 — Personal data for verification by the authentic source interface.
#
# Grants the ASIP (ETSI TS 119 478) the BRP fields behind the attributes it
# verifies and retrieves: name, date of birth, nationality and a resident's
# address. The ASIP has checked the citizen's access token and puts the BSN
# from it in the query; the source sees the ASIP, not the citizen.
#
# Not checked here: the access token, its scope and the QTSP behind it. The
# ASIP checks those before it asks.

import data.gbo.lib

rule_id := "ASI0001"

# Bound only without a consent token; with one the consent rules judge the
# request. This decides whose reasons are reported, not access: with a
# token, PID_NOT_PRESENT below denies anyway.
regime := "pid"

# Query.ingeschrevenPersoon is also the root of bri; this rule judges brp only.
services := {"brp"}

# The ASIP's FSC peer.
asip := "99999999900000001200"

# Every field is listed: other BRP fields (heeftHuwelijk, gezag, a
# non-resident's address) fall to the engine's closed world. Interface fields
# are keyed by IngeschrevenPersoon, the address by Ingezetene, the only type
# with a Binnenlandsadres.
covers_types := set()

covers_fields := {
	"Query.ingeschrevenPersoon",
	"IngeschrevenPersoon.geslachtsnaam",
	"IngeschrevenPersoon.voorvoegsel",
	"IngeschrevenPersoon.voornamen",
	"IngeschrevenPersoon.geboortedatum",
	"IngeschrevenPersoon.heeftNationaliteit",
	"Nationaliteit.nationaliteit",
	"Ingezetene.woontOp",
	"Binnenlandsadres.straatnaam",
	"Binnenlandsadres.huisnummer",
	"Binnenlandsadres.huisletter",
	"Binnenlandsadres.huisnummertoevoeging",
	"Binnenlandsadres.postcode",
	"Binnenlandsadres.woonplaatsnaam",
}

# ── The caller ───────────────────────────────────────────────────────────────

deny contains "PID_NOT_PRESENT" if data.gbo.consent.resolved

deny contains "ACTOR_NOT_ALLOWED" if lib.acting_party != asip

# ── The question ─────────────────────────────────────────────────────────────

# The BSN from the access token, in the root field's argument: not empty and
# not a placeholder, which stands for the subject of a consent.
deny_field[i] contains "PID_NOT_PRESENT" if {
	some i, f in lib.fields
	lib.key(f) == "Query.ingeschrevenPersoon"
	not _names_a_citizen(f)
}

_names_a_citizen(f) if {
	value := lib.argument(f, "bsn")
	is_string(value)
	value != ""
	not value in lib.subject_placeholders
}
