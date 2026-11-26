# lvg-graphql-server

## Purpose

The mock LVG (Landelijke Voorziening Gebouwen) of the LVG/Installatie Register
pilot. `vbo(bsn, vboId)` answers the asked VBO-id when the citizen owns that
verblijfsobject and `null` otherwise, so the caller learns yes or no and nothing
about other buildings. Data: `mockdata/owners.json`. DvTP only: no
`/.well-known/gbo`, not onboarded. FSC service `lvg` on BD's provider peer,
behind `lvg-sidecar`; compose profiles `dvtp` and `full`.

## Contracts

- The one caller is the Installatie Register (`ir-backend`, a
  `dienstverlener-backend` with `QUERY_KIND=lvg`). Policy `LVG0001`
  (`policies/gbo/rules/dvtp/lvg0001.rego`) names `Query.vbo` and
  `Verblijfsobject.vboId` and pins the scope `lvg:vbo:eigendom`; a schema change
  goes with that rule and the PDP's copy
  ([../../schemas/AGENTS.md](../../schemas/AGENTS.md)).
- LDV: one record per ownership check, also for a `null` answer, under
  `LDV_ACTIVITY_BASE` plus `/lvg-vbo-eigendom/v1`, an activity in
  `services/ldv-logboek/config/verwerkingsactiviteiten-lvg.json`.
  `LDV_SCOPE_ACTIVITY_BASE` is the old name, kept as a fallback. Fail-closed as
  in [../graphql-server](../graphql-server/AGENTS.md).

## Verify

`go test ./...`; after a schema change `scripts/check-schema-drift.sh` and
`make policy-test`. End to end: `make demo-dvtp`, then "Demo" in
[../../docs/lvg-ir.md](../../docs/lvg-ir.md) (Dutch), which also describes the
pilot flow; open it before changing the question or its caller.

## Decisions

- The ownership question with the VBO-id in the query, answered with that id or
  `null`: [#435](https://github.com/ICTU/GBO-demo/pull/435).
- Activity named by this source, not by a scope header:
  [#580](https://github.com/ICTU/GBO-demo/pull/580).
