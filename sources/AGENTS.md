# sources

## Purpose

The operator-managed source configurations the reconciler
(`eudi-adapter reconcile-sources --sources-dir`) onboards; a file here is the
only thing that onboards a source. Each says where the source document is and
how it is fetched, nothing else: identity comes from the certificate set, access
from FSC contracts, content from the document. This node also covers
`source-metadata/`, the directory for documents of sources that publish none
(`transport: file`).

## Contracts

- The file name without `.yaml` is the `source_id`, unique across both
  subdirectories, and the key of the source's certificate set (issuer, reader
  and status leaves, which `make onboard-demo-sources` makes for the demo
  sources).
- The reconciler reads `*.yaml` here and one directory down, and refuses the
  whole set when one file is invalid or two name the same metadata endpoint. A
  complete release is promoted only when every configured source is usable, so
  one broken source holds back every other source's changes.
- `configured/` is what every deployment mounts; `local-demo/` (the unsecured
  demo source) is mounted only by local compose.
- `provider_peer_id` goes on the leg that speaks FSC and on one leg only.
  Allowed legs, metadata then data: `fsc`+`fsc` (two different services),
  `unsecured`+`fsc`, `unsecured`+`unsecured`, `file`+`fsc`, `file`+`unsecured`.
  Without `data_access` the data leg uses the metadata transport, so `file`
  always needs it.
- A `file` document sits under `source-metadata/`, at a path that may not leave
  it. It still passes the schema and the OIN check; its `expires_at` must stay
  at least an hour ahead or the source turns `stale`, then `blocked`.

## Verify

`make onboard-demo-sources` runs one reconciliation with auto-promote, then
`docker compose --profile onboarding run --rm source-reconciler ./eudi-adapter inspect-source-registry`
shows the active release: its sources, versions and freshness.

## Decisions

- Manual configuration as the only onboarding trigger:
  [#237](https://github.com/ICTU/GBO-demo/pull/237).
- FSC Peer ID in the configuration, OIN and name from the certificates:
  [#242](https://github.com/ICTU/GBO-demo/pull/242).
- Metadata and data leg chosen separately, `file` transport:
  [#318](https://github.com/ICTU/GBO-demo/pull/318).

## Links

- [../docs/source-onboarding.md](../docs/source-onboarding.md) (Dutch),
  "Transportprofielen" and "Lokaal": the profiles and the local steps; open
  before adding or changing a source.
- [../source-metadata/README.md](../source-metadata/README.md): who signs off on
  a `file` document.
