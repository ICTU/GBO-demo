# Security

This repository is a **demo**. All cryptographic material, credentials, and identifiers in this codebase are self-signed, well-known, or synthetic. There is no user data to protect and no production system behind it.

## Reporting a security issue

If you find something that looks like a real security problem — for example a leaked credential that turns out not to be a well-known default, or a vulnerability in the demo services that could be exploited if someone deployed this code as-is — please report it privately.

- Open a private security advisory via GitHub (**Security → Advisories → New draft security advisory**).
- Or email the repository maintainer directly.

Please do **not** open a public issue for suspected security problems. We'll respond as quickly as we can.

## What is intentionally exposed

- **Self-signed certificates** under `fsc-infra/**/pki/` and `certs/` — used only by the demo containers on localhost. Private-key files (`*-key.pem`) are git-ignored and regenerated per-user via the `make fsc-*-certs` targets.
- **Synthetic OIN's** (mostly `9999...` and `0000...` prefixes) — reserved for demo, do not correspond to real Dutch organizations.
- **Mock BSN's** in `services/graphql-server/mockdata/citizens.json` — checksum-valid demo numbers, not linked to real citizens.
- **JWT signing secrets** for mock DigiD — hardcoded in the portal, single-purpose demo.

These are safe to expose publicly for their intended use. If you're deploying any component beyond a local demo, replace all of the above.

## What is deliberately NOT in the repo

The EUDI issuance-server runtime config carries inline mdoc/SD-JWT signing keys plus hostnames tied to the issuer/reader certificates. All four runtime files are git-ignored; committed `.example` twins carry placeholder keys and generic `example.com` hostnames:

- `services/eudi-issuance-server/config/issuance_server.toml`
- `services/eudi-issuance-server/config/inkomensverklaring_metadata.json`
- `services/eudi-issuance-server/config/issuer_auth.json`
- `services/eudi-issuance-server/config/reader_auth.json`

Bootstrap all four with `make eudi-config` (copies from `$NLWALLET_PATH/target/is-config/`) or by hand from the committed `.example` files.

The safety-audit script (`scripts/check-safety.sh`) refuses inline `private_key = "…"` / `certificate = "…"` patterns in any committed file, so this stays enforced.
