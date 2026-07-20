package dvtp.authz

# Entry-point for the PEP's `/v1/data/dvtp/authz` call. Delegates to the
# GBO rule-engine runtime in dvtp.gbo, which evaluates the rules
# (./gbo/rules/*.rego) against the input.resolved that the PDP-handler
# enriched.
#
# Two reasons for this thin entry-point instead of letting the PEP talk
# to dvtp/gbo directly:
#   1. Backwards-compat: the PEP knows the path /v1/data/dvtp/authz
#      (since AuthZEN-envelope adoption); renaming would require a PEP
#      redeploy.
#   2. Ability to place a second engine version alongside dvtp.gbo in
#      the future and make the selection here.
#
# `decision` + `context` are the two fields the wire-shape defines. The
# PEP only reads `decision` (and `context.reason_admin.code` on DENY);
# the dev-portal UI uses `context.granted[]` / `context.denied_fields[]`.

import data.dvtp.gbo

decision := gbo.response.decision

context := gbo.response.context
