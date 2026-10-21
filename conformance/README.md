# Conformance — ETSI TS 119 478

The conformance claim of the ASI route (epic #336, slice 6): for every requirement of ETSI TS 119 478 V1.1.1, how the demo meets it.

| File | |
|---|---|
| `etsi-ts-119478-ids.txt` | the requirement IDs of the specification, in document order: identifiers only, no requirement text |
| `etsi-ts-119478.yaml` | the matrix: a status per ID, or per group of IDs with a common prefix; an entry overrides its group |
| `check/` | the check that runs in CI |

A status is one of:

- `passed`: the tests named prove it;
- `stub`: the tests named prove it against a stub that is not conformant; `reason` says which;
- `not-applicable`: it does not apply to this ASIP; `reason` says why;
- `failing`: not met; `reason` says why and `slice` where it will be met.

A test is `<dir>:<TestName>`, or `<dir>:<TestName>/<subtest>`, with the directory relative to the repository root. A subtest cannot be listed without running the tests, so the check requires its name (each part of a nested one) to be a string literal in a test file of that directory: a removed or misspelt subtest is caught, a renamed literal that still exists elsewhere is not.

The check fails when an ID of the list has no entry and no group, when an entry or group names an ID that is not in the list, when a status lacks what it requires, or when a named test does not exist. It does not run the tests: the services' CI jobs do. On success it prints the count per role (ASIP, AZSP, AZP, DIP, TSP) and status.

```bash
cd conformance/check && go run . -matrix ../etsi-ts-119478.yaml -root ../..
```

The roles DIP (the Discover Interface Provider) and TSP (the trust service provider that asks) are outside the ASIP's claim; the demo plays them with `services/asi-discovery-stub` and `services/qtsp-reference`.

The security considerations the authorization server must take into account (REQ-AZSP-6.1.3.4-01) are weighed in [`security-considerations.md`](security-considerations.md).
