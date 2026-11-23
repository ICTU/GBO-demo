# ldv-logboek

## Contracts

- `LDV_LOGBOOK_ID` must equal what producers send as `dpl.read.nextLogbookId`
  (`LDV_NEXT_LOGBOOK_IDS`) and the key in `dev-portal-backend`'s `LDV_LOGBOOKS`.
  Nothing checks this.

## Patterns

New instance: register file in `config/`, compose service with its own volume
and `LDV_LOGBOOK_ID`, an `LDV_LOGBOOKS` entry. New activity: register entry,
then its URI in the producer's environment.

## Pitfalls

- No migrations (`CREATE TABLE IF NOT EXISTS`): a schema or storage-format
  change needs a volume reset, as in
  [#652](https://github.com/ICTU/GBO-demo/pull/652).

## Verify

`go test ./...` here and in `../ldv-client`; `make policy-test` when the FSC
read changes.

## Decisions

- Plain HTTPS+JSON, not OTLP, so the confirmation is explicit:
  [#303](https://github.com/ICTU/GBO-demo/pull/303).
- One logbook per Verantwoordelijke:
  [#304](https://github.com/ICTU/GBO-demo/pull/304).
- Read extension per its OpenAPI, separate read and write tokens:
  [#305](https://github.com/ICTU/GBO-demo/pull/305),
  [#317](https://github.com/ICTU/GBO-demo/pull/317).
- Token-free reduced read over FSC, capped at 500:
  [#705](https://github.com/ICTU/GBO-demo/pull/705).

## Open questions

- `ldv.Query` says "exactly one" selector; `normalize` accepts several. Which is
  intended?
- README shows activities as `bd-ib-2025@v1`; code and compose use
  `/{id}/{version}` URIs. Is the README stale?
- `logboek-levering-*` load the source's full register, so accept all its
  activities. Intended?

## Links

- [README.md](README.md): record, API, configuration, producers and the client;
  open before any behaviour change.
- [../../docs/ldv/README.md](../../docs/ldv/README.md) (Dutch): following a
  chain across logbooks; open when changing trace or pointer fields.
