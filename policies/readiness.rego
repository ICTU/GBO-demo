package doelbinding.readiness

# Readiness of the PDP itself, asked by its container healthcheck
# (services/openftv-pdp/pdp-ready). Not an authorization decision: nothing
# but the probe sends doelbinding "readiness".
#
# Ready means the PDP can decide what the FSC source Managers ask first:
# whether to auto-sign a contract. That needs the policies, wherever they
# come from (./policies or the Manager's bundle), and the onboarding
# register, which OpenFTV pulls into data.entities after it has started.
# Until the first pull the auto-sign policy refuses every contract with
# ADMISSION_REGISTER_UNAVAILABLE.
#
# OpenFTV's /readyz cannot say this: at v0.1.0 it answers like /healthz,
# and its ready flag covers the bundle download but not the PIP pulls.
#
# Which parties the register admits does not matter here: revoking an
# admission is a decision of the register, not a fault of the PDP.

import data.doelbinding.auto_sign_contract

default allow := false

allow if auto_sign_contract.register_available
