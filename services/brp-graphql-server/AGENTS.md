# brp-graphql-server

## Purpose

The mock BRP source of RvIG (FSC service `brp`): the BRP bronprofiel of
gbo-semantiek v0.3 from `mockdata/personen.json`, plus `akteVanOverlijden(bsn)`
for the "Akte van overlijden" attestation. It publishes its source document,
`config/gbo-source-metadata.json` (version 1.1, with `eudi` and `asi`), at
`/.well-known/gbo`, through its own FSC service `gbo-metadata-rvig` straight at
this server. Callers: the EUDI issuance and the ASIP (`asi-provider`), both
through `brp-sidecar`.

## Contracts

- `akteVanOverlijden` holds the selection (which marriage, which deceased
  partner) so the published mapping stays plain JSON pointers and GBO needs no
  BRP-specific conversion. Its field names are the claim pointers in the
  document, whose query names every field.
- The document's `asi` query is `ingeschrevenPersoon(bsn) { ...Selected }`; the
  ASIP expands `...Selected` from the field pointers (`/woontOp/straatnaam`) and
  type names (`on: Ingezetene`) in the document. Onboarding checks the
  document's shape, not this schema, so a renamed field or type fails only at
  the first ASI request.
- The document follows the same version rules as
  [../graphql-server](../graphql-server/AGENTS.md); `asi-mapping`,
  `eudi-adapter`, `asi-provider`, `qtsp-reference` and
  `asi-authorization-server` tests read it.
- `mockdata/personen.json` is also read by `test-pid-source` (it stops at
  startup on a person it cannot map, such as a nationality other than NL, BE or
  DE), `dev-portal-backend` (`internal/testpid`) and `eudi-credential-issuer`.
- LDV: the activities are constants in `ldv_query.go`, matching
  `services/ldv-logboek/config/verwerkingsactiviteiten-brp.json`. An akte writes
  one record for the asker and one per living parent of the deceased, named by
  `brp-persoon-id`. Fail-closed as in `graphql-server`.
- graphql-go has no interface-implements-interface, so `Partij` and
  `IngeschrevenPersoon` stand side by side here; the PDP's copy keeps the
  relation and the drift check allows that one difference.

## Verify

`go test ./...`; after a schema change `scripts/check-schema-drift.sh`; after a
`config/` change the tests of the modules above; after a `mockdata/` change
`go test ./...` in `../test-pid-source`. End to end: `make demo-eudi` (akte) and
`make demo-asi` (ASI).

## Decisions

- Source-owned attestation view instead of conversions in GBO:
  [#219](https://github.com/ICTU/GBO-demo/pull/219),
  [#204](https://github.com/ICTU/GBO-demo/pull/204).
- Same `Query.ingeschrevenPersoon` as BD, separate FSC services and schemas:
  [#125](https://github.com/ICTU/GBO-demo/pull/125),
  [#482](https://github.com/ICTU/GBO-demo/pull/482).
- The relatives in an akte are Betrokkenen with records of their own:
  [#304](https://github.com/ICTU/GBO-demo/pull/304).
- Publishes `asi`: [#613](https://github.com/ICTU/GBO-demo/pull/613).

## Links

- [../../schemas/AGENTS.md](../../schemas/AGENTS.md): the PDP's copy of this
  schema; open before changing `buildSchema`.
- [../../docs/source-onboarding.md](../../docs/source-onboarding.md) (Dutch),
  "Attributen voor verificatie (`asi`)": the `asi` format; open before changing
  that part of the document.
