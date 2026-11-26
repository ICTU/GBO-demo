# graphql-server

## Purpose

The mock Belastingdienst source (FSC service `bri`): a subset of the BD
bronprofiel of gbo-semantiek v0.3, the IH aangiften of an `IngeschrevenPersoon`,
from `mockdata/citizens.json`. It also publishes its source document,
`config/gbo-source-metadata.json`, at `/.well-known/gbo`. The same image runs as
`unsecured-graphql-server`, the deliberately unauthenticated demo source:
`config/gbo-source-metadata-unsecured.json`, no logbook, not on FSC. No GraphiQL
or playground here; the developer portal's playground reads the introspection.

## Contracts

- `heeftBelastingjaarAangifte(belastingjaren:)` is a demo extension of the
  upstream schema: the PDP enforces per-year consent on that argument
  (`YEAR_NOT_COVERED`, `policies/gbo/rules/dvtp/dvt0001.rego`), so the year must
  travel inside the query.
- The documents in `config/` are what onboarding fetches, read once at startup
  (a change needs a restart). A changed document needs a higher `version`: the
  reconciler refuses other bytes under the same version, and a lower one. Change
  `claims` or `display` and raise `type_version` too. The tests of
  `asi-mapping`, `eudi-adapter` and `asi-provider` read these files.
- `/.well-known/gbo` is reached through its own FSC service (`gbo-metadata-bd`)
  pointing straight at this server, not through `bron-sidecar`, which forwards
  `POST /graphql` only.
- `LDV_LOGBOOK_URL` set: the answer is held until its records are spooled, and
  withheld (500) when spooling fails. Unset: no records, and `sourceLogbook` is
  nil, so its methods stay nil-safe. One record per requested year (no filter:
  every year served), under `LDV_YEAR_ACTIVITY_TEMPLATE`. An activity missing
  from `services/ldv-logboek/config/verwerkingsactiviteiten-bd.json` does not
  fail the request, whatever `activityForYear` says: the logbook refuses it at
  delivery, which blocks the outbox (see
  [../ldv-client](../ldv-client/AGENTS.md)).
- `mockdata/` is also mounted into `dev-portal-backend` (`/citizens`).

## Verify

`go test ./...`; after a schema change `scripts/check-schema-drift.sh`; after a
`config/` change `go test ./...` in `../asi-mapping`, `../eudi-adapter` and
`../asi-provider`. End to end: `make demo-eudi` issues from the onboarded
document, `make demo-dvtp` queries through the sidecar.

## Decisions

- Per-year consent in policy, on the `belastingjaren` argument:
  [#58](https://github.com/ICTU/GBO-demo/pull/58).
- Playgrounds removed in favour of one in the developer portal:
  [#142](https://github.com/ICTU/GBO-demo/pull/142).
- The unsecured source stays local-only (`sources/local-demo/`):
  [#237](https://github.com/ICTU/GBO-demo/pull/237).

## Links

- [../../schemas/AGENTS.md](../../schemas/AGENTS.md): the PDP's copy of this
  schema; open before changing `buildSchema`.
- [../../docs/source-onboarding.md](../../docs/source-onboarding.md) (Dutch),
  "Claims en Type Metadata": the format of the documents in `config/`; open
  before editing them.
