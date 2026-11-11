# Conformance

Conformance claims: for every requirement of a specification, how the demo meets it.

- **ASI route** (epic #336, slice 6) against ETSI TS 119 478 V1.1.1.
- **Own credential issuer** (`services/eudi-credential-issuer`, #614) against OpenID4VCI 1.0 and HAIP 1.0, per wallet profile (`nl`, `eudi`, `wallet`). The default status is the `eudi` profile's; `nl` and `wallet` differ where they say so. The issuer is Credential Issuer, its own Authorization Server, and Verifier for disclosure-based issuance. It issues SD-JWT VC only.

| File | |
|---|---|
| `etsi-ts-119478-ids.txt`, `etsi-ts-119478.yaml` | ETSI TS 119 478: the requirement IDs in document order (identifiers only, no requirement text), and the matrix |
| `oid4vci-1.0-ids.txt`, `oid4vci-1.0.yaml` | OpenID4VCI 1.0: the normative statements as IDs with their role, and the matrix |
| `haip-1.0-ids.txt`, `haip-1.0.yaml` | HAIP 1.0: idem |
| `check/` | the check that runs in CI |
| `check/extract/` | makes the ID list of an OpenID specification: one ID per sentence with MUST, SHALL, SHOULD, REQUIRED or RECOMMENDED (not MAY), numbered per section (`OID4VCI-8.2-03`); `-roles` keeps the roles of an earlier list |

The roles of the OpenID lists are `issuer`, `as` (authorization server), `verifier`, `wallet`, `ecosystem` and `other` (another party, such as a wallet provider). A statement worded for the wallet but enforced by the issuer has the issuer's role. Requirements on wallets, ecosystems and other parties are outside the claim.

A status is one of:

- `passed`: the tests named prove it;
- `stub`: the tests named prove it against a stub that is not conformant; `reason` says which;
- `not-applicable`: it does not apply to this ASIP; `reason` says why;
- `failing`: not met; `reason` says why and `slice` where it will be met;
- `deviation`: not met on purpose; `reason` says why and for which wallet. Tests may show the behaviour instead.

The check serves other specifications too, with the same rules. An ID list may give each ID a role after it (`OID4VCI-8.2-03 issuer`); without one the role comes from the ID, as for ETSI (`REQ-<role>-…`). A group covers IDs by `prefix`, by `role`, or both; an entry overrides it, and of two groups the one with the longer prefix wins, then the one with a role. A matrix that lists `profiles` (for example `[nl, eudi, wallet]`) may give an entry or a group another status per profile under `profiles:`, with the same rules; the summary is then per profile and role.

A test is `<dir>:<TestName>`, or `<dir>:<TestName>/<subtest>`, with the directory relative to the repository root. A subtest cannot be listed without running the tests, so the check requires its name (each part of a nested one) to be a string literal in a test file of that directory: a removed or misspelt subtest is caught, a renamed literal that still exists elsewhere is not.

The check fails when an ID of the list has no entry and no group, when an entry or group names an ID that is not in the list, when a status lacks what it requires, or when a named test does not exist. It does not run the tests: the services' CI jobs do. On success it prints the count per role (ASIP, AZSP, AZP, DIP, TSP) and status.

```bash
cd conformance/check && go run . -matrix ../etsi-ts-119478.yaml -root ../..
cd conformance/check && go run . -matrix ../oid4vci-1.0.yaml -root ../..
cd conformance/check && go run . -matrix ../haip-1.0.yaml -root ../..
```

To make an OpenID ID list again, from the specification's HTML:

```bash
cd conformance/check && go run ./extract -spec <spec.html> -prefix OID4VCI -title "OpenID for Verifiable Credential Issuance 1.0" -roles ../oid4vci-1.0-ids.txt > /tmp/ids.txt
```

The roles DIP (the Discover Interface Provider) and TSP (the trust service provider that asks) are outside the ASIP's claim; the demo plays them with `services/asi-discovery-stub` and `services/qtsp-reference`.

The security considerations the authorization server must take into account (REQ-AZSP-6.1.3.4-01) are weighed in [`security-considerations.md`](security-considerations.md).

PID certificate trust is supplied by the centrally signed GBO PID LoTE in
configured internal consumers; see [trust boundaries and demo shortcuts](../docs/gbo-trust-lists.md#internal-pid-consumers).
This changes trust-source configuration, not the claimed HAIP or OpenID4VCI
requirements. The partial ETSI JSON support does not add a TS 119 602 or JAdES
conformance claim.
