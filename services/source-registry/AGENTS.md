# source-registry

## Purpose

A Go library, no image of its own: the Source Registry's model and use cases
(`onboarding`), its PostgreSQL store (`postgresregistry`) and the reader of a
release's `asi` capability (`asisnapshot`). `eudi-adapter` links it as
reconciler (the writer), runtime adapter and issuance materializer (readers);
`asi-provider` reads releases through `asisnapshot`. Both use a `replace` to
`../source-registry` and build from the repository root, so a change here
rebuilds both images. What a source document means (schema, mapping, Type
Metadata, transports) is decided in `eudi-adapter`; this module stores and
versions what it produced.

## Contracts

- `onboarding` imports no transport or persistence package;
  `TestCoreHasNoInfrastructureImports` fails otherwise.
- A release ID is the SHA-256 of `releaseDigestDocument`, which leaves out the
  lifecycle timestamps (a `304` refresh updates them in place) and
  `data_transport_authenticated` (added after releases existed). A field added
  to that document gives the same material a new ID, which auto-promote
  activates, also over an operator's rollback.
- Migrations are `postgresregistry/migrations/NNN_name.sql`, embedded and
  applied once per number under an advisory lock: an edit to an applied file
  never runs. A table the runtime reads needs a grant in `GrantReadOnly`;
  candidates and statuses stay invisible to that role.
- Snapshot `schema_version` is `3.0` without `asi` and `4.0` with it.
  `eudi-adapter` writes it and `asisnapshot.Read` refuses any other, so a new
  version changes both.
- No private key enters the registry: `rejectSecretJSON` refuses fields named
  like `private_key` or `*key_reference` and PEM private keys in snapshots and
  offers.

## Pitfalls

- Auto-promote re-activated an existing release over an operator's rollback;
  `Promote` now moves the pointer only for a new release:
  [#276](https://github.com/ICTU/GBO-demo/pull/276).

## Verify

`go test ./...` here; the store tests skip without
`SOURCE_REGISTRY_TEST_DATABASE_URL`, which the CI job `source-registry` sets for
a PostgreSQL 16. Locally:

```bash
docker run -d --rm --name registrytest -e POSTGRES_PASSWORD=registrytest -p 55432:5432 postgres:16-alpine
SOURCE_REGISTRY_TEST_DATABASE_URL="postgres://postgres:registrytest@127.0.0.1:55432/postgres?sslmode=disable" \
  go test -count=1 ./postgresregistry
```

Then `go test ./...` in `../eudi-adapter` and `../asi-provider`.

## Decisions

- PostgreSQL with immutable releases and one active pointer, instead of a shared
  volume: [#276](https://github.com/ICTU/GBO-demo/pull/276).
- A module of its own, so the ASIP reads the same model:
  [#621](https://github.com/ICTU/GBO-demo/pull/621),
  [#622](https://github.com/ICTU/GBO-demo/pull/622).
- Snapshot `4.0` for `asi`, so an adapter that knows only `3.0` does not rewrite
  it: [#612](https://github.com/ICTU/GBO-demo/pull/612).
- Metadata and data leg authenticated separately:
  [#318](https://github.com/ICTU/GBO-demo/pull/318).

## Links

- [../../docs/source-onboarding.md](../../docs/source-onboarding.md) (Dutch):
  states, promotion, rollback and the production roles; open before changing
  promotion, activation or statuses.
- [../../docs/source-metadata-cache.md](../../docs/source-metadata-cache.md)
  (Dutch): freshness, stale grace and which process uses which account; open
  before changing lifecycle fields or grants.
