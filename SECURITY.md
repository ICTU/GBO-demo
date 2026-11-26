# Security policy

## Current status

This repository is a **reference-architecture demo**. It has not been hardened for production use. We advise against running the demo internet-facing or in an otherwise untrusted environment.

All cryptographic material, credentials, and identifiers in this codebase are self-signed, well-known, or synthetic. There is no user data to protect and no production system behind it.

## Supported versions

Only the latest version on `main` is supported with security updates.

## Reporting a vulnerability

If you find something that looks like a real security problem — for example a leaked credential that turns out not to be a well-known default, or a vulnerability in the demo services that could be exploited if someone deployed this code as-is — please report it privately.

- Open a private security advisory via GitHub (**Security → Advisories → New draft security advisory**), or
- email the repository maintainer at <jeroen.dekok@ictu.nl>.

Please do **not** open a public issue for suspected security problems. We aim to respond within 24 hours with a confirmation and a brief action plan or a request for more information.

## What is intentionally exposed

- **Self-signed certificates** under `fsc-infra/**/pki/` and `services/openftv-pdp/certs/` — used only by the demo containers on localhost. Private-key files (`*-key.pem`) are git-ignored and regenerated per user by the AppHost's `<peer>-certs` steps (`fsc-infra/pki/bootstrap-peer.sh`, one row per party in `fsc-infra/peers.txt`).
- **Synthetic OIN's** (mostly `9999...` and `0000...` prefixes) — reserved for demo, do not correspond to real Dutch organizations.
- **Mock BSN's** in `services/graphql-server/mockdata/citizens.json` — checksum-valid demo numbers, not linked to real citizens.
- **JWT signing secrets** for mock DigiD — hardcoded in the portal, single-purpose demo.

These are safe to expose publicly for their intended use. If you're deploying any component beyond a local demo, replace all of the above.

## What is deliberately NOT in the repo

The EUDI issuance-server runtime config carries inline mdoc/SD-JWT signing keys plus hostnames tied to the issuer/reader certificates. Runtime artifacts are generated from active source registrations and are git-ignored:

- `services/eudi-issuance-server/config/issuance_server.toml`
- `services/eudi-issuance-server/config/type-*.json`
- `services/eudi-issuance-server/config/eudi-offers.json`

Start the AppHost with the `eudi` profile; its `eudi-issuance-materialize` step validates every configured source, generates artifacts from the complete candidate set and promotes that set to active. Local development certificates are created only by the AppHost's explicit `provision-<source>-certificates` steps. Production must supply pre-managed certificate references.

The safety audit (`scripts/check-safety.sh`) fails on inline `private_key = "…"` / `certificate = "…"` values, other secrets, real OINs (organisatie-identificatienummers) and internal hostnames in any tracked file. CI runs it on every change.
