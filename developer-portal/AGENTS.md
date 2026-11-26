# developer-portal

## Purpose

Demo tooling for developers, not a citizen frontend: a React/Vite single-page
app that runs the flows (DvTP issuance and use, EUDI, ASI), colours the
architecture strip live, and shows per run the trace, the PDP decision, the FSC
transaction logs and the LDV chain, plus history, scenarios, the source GraphQL
playground and revocation. It has no server logic of its own: the reads, the
live span stream and the history sit in
[../services/dev-portal-backend](../services/dev-portal-backend/AGENTS.md)
(`/api/dev/`).

## Contracts

- Two ways of serving with the same proxy routes: Compose runs the image's
  `build` stage as the Vite dev server (`vite.config.ts`, targets from
  `*_TARGET`, `src/` mounted), the image's final stage is nginx (`nginx.conf`,
  fixed Compose names). A new route goes in both, unbuffered, because
  `/api/dev/events` and `/api/dev/watch-next` are SSE.
- `/bron-api/<source>/` paths equal `graphqlPath` in `src/data/bronnen.ts`. Its
  `gatewaySvc` and `bronSvc`, and the services in `src/data/chains.ts` and
  `src/util/consumer.ts`, must match the `OTEL_SERVICE_NAME`s and services in
  `docker-compose.yml`; spans are mapped to nodes by name.
- Public URLs: the image writes `window.__GBO_RUNTIME_CONFIG__` at start
  (`docker-entrypoint.d/40-runtime-config.sh`); code reads it first, then
  `import.meta.env.VITE_*`. A new URL goes in the script, `src/vite-env.d.ts`,
  its reader and Compose. Everything here ends up in the browser: no secrets.
- Correlation: the trace id finds the LDV chain; the span tag
  `gbo.fsc.transaction_id` finds the FSC transaction logs and the PDP decision.
  The ADL record is the decision and colours the PDP; the Loki console line is
  detail only (`OBSERVABILITY.md`). All arrive late, so the hooks poll for
  seconds (`useDecisions`, `useLdvChain`).
- Requests it starts carry `X-Demo-Source: dev-portal` (the consent portal and
  consumer backends then skip their own history entry; the portal logs the run)
  and `X-Demo-Session` from the `gbo_demo_session` cookie, which is forgeable:
  never gate data on it. History is filtered by session because the Use form
  prefills the latest consent from it.
- A scope `lvg:*` goes through `ir-backend` (`/ir-api`), any other through
  `dienstverlener-backend` (`consumerFor`).

## Pitfalls

- `crypto.randomUUID` is missing outside a secure context, which broke every
  button over plain http from another machine:
  [#218](https://github.com/ICTU/GBO-demo/pull/218).

## Verify

`npx tsc --noEmit && npm test` (CI runs both). In the stack, after a branch
switch: `docker compose up -d --force-recreate developer-portal`, else the bind
mounts can be stale. Empty DvTP tabs are expected with `make demo-minimal` and
`make demo-eudi`.

## Decisions

- Watch mode and history bound to a demo session:
  [#207](https://github.com/ICTU/GBO-demo/pull/207).
- One GraphQL playground for every source, in the portal, queried same-origin
  (no published source ports, no CORS):
  [#142](https://github.com/ICTU/GBO-demo/pull/142).
- Console decision line found by the `Fsc-Transaction-Id` header:
  [#493](https://github.com/ICTU/GBO-demo/pull/493).
- ASI tab lays the request down at the reference QTSP through the backend:
  [#539](https://github.com/ICTU/GBO-demo/pull/539).
- Revocation off unless `CREDENTIAL_ISSUER_INTERNAL_URL` is set, test persons
  only: [#698](https://github.com/ICTU/GBO-demo/pull/698).

## Links

- [../OBSERVABILITY.md](../OBSERVABILITY.md): traces, Loki, ADL and runtime
  variables; open before changing a correlation panel.
- [../docs/debugging-eudi-wallet.md](../docs/debugging-eudi-wallet.md): when an
  EUDI QR or wallet run fails.
