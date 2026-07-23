# BSN masking for EDI decision-logs.
#
# OPA reads `data.system.log.mask` automatically when emitting decision-
# logs; each set-entry is a JSON-pointer that gets removed from the
# logged record. BSN travels through the EUDI-flow (self-service,
# receiver-specific) but must not end up in the audit stream. The DvTP-
# flow contains no BSN in the input (the PEP-boundary strips it), so
# these mask-rules target EUDI-facing inputs.
#
# JSON-pointers target the places where BSN can come from:
#   /input/resource/bsn           — PEP puts it there in the EUDI branch
#   /input/pip/pid/bsn            — PDP-handler enriches via pipData
#   /input/resource/variables/bsn — adapter builds query with $bsn variable
#   /input/resolved/args          — PDP-resolved args-map with key names
#                                    like "vars.bsn" and "bsn" (dots in
#                                    keys); OPA's mask-processor does not
#                                    parse JSON-pointers per key cleanly
#                                    for dots — masking the whole map is
#                                    more robust than per-key targeting.
#                                    Consequence: consent_id also
#                                    disappears from args, but that is
#                                    not audit-limiting (consent_id also
#                                    lives at /input/resource/consent_id,
#                                    which is NOT masked).
package system.log

mask contains "/input/resource/bsn"

mask contains "/input/pip/pid/bsn"

mask contains "/input/resource/variables/bsn"

mask contains "/input/resolved/args"
