# dienstverlener-mock

## Purpose

Hypotheek-BV's citizen frontend (port 9001) in the DvTP consent flow: the start
page asks consent, `/return` completes it and shows the income data. It holds no
consent logic and no token: everything goes through
[`dienstverlener-backend`](../services/dienstverlener-backend/README.md) at
`/api`, the Vite proxy (`DV_API_TARGET`) in Compose and `nginx.conf` in the
published image. No runtime configuration: the backend builds the portal
address.

## Contracts

- No consent token, code or request in browser storage or in a request body: the
  query sends `{}` and the backend uses its session's token.
  `tests/consentFlow.test.mjs` checks the sources for it.
- `Return` reads the code (or error) and the state once, clears them with
  `history.replaceState` and calls `completeConsent` once; `index.html` sends no
  referrer, so the code never reaches a `Referer`.
- `/return` must equal the backend's `CONSENT_REDIRECT_URI` and the return
  location registered in `services/dvtp-onboarding-register/config/demo.json`.
- `CONSENT_REQUEST` in `pages/Start.tsx` must name a service registered for
  Hypotheek-BV; the name and purpose the citizen sees come from that
  registration.
- `src/lib/consentFlow.ts` is a copy of the keyper-mock's; change both.
- The citizen sees only what `denialMessage` names by `denial_code`; any other
  code gets the generic text, which never mentions consent. What passes is
  decided in the backend's `consumer/denial.go`; never show `reason`.
- The browser leaves the app only for the backend's `redirect_to`, and only when
  it is an absolute http(s) URL (`portalAddress`).
- `/api/dvtp/query` carries `X-Demo-Session` from the developer portal's
  `gbo_demo_session` cookie (`lib/demoSession.ts`); `dienstverlener-backend`
  puts it on its span as `gbo.demo.session`, and the developer portal's watch
  mode matches on it. Start and complete carry none, so their traces wake every
  watcher. Forgeable: never gate anything on it.
- `nginx.conf` proxies `/api/` to the fixed `dienstverlener-backend:4006`:
  wherever the image runs, the backend must answer under that name and port. The
  simulation deployment runs both in one namespace, under `deploy/helm/gbo-app`
  with `healthPath: /` (no `/healthz` here).

## Pitfalls

- The FSC Inway drops the PDP's reason, so every refusal shows the generic
  message until upstream fixes it
  ([OpenFSC #308](https://gitlab.com/rinis-oss/fsc/open-fsc/-/issues/308),
  README "What a denial tells the citizen").

## Verify

`npx tsc --noEmit && npm test` (what CI runs). End to end: `make demo`, start at
`:9001`, grant, then refuse in the portal.

## Decisions

- Pushed request and one-time code, token in the backend session:
  [#693](https://github.com/ICTU/GBO-demo/pull/693).
- Citizen text by code only, no cause:
  [#489](https://github.com/ICTU/GBO-demo/pull/489).

## Links

- [../docs/frontends.md](../docs/frontends.md): serving, runtime configuration
  and npm policy shared by the frontends; open before adding a proxy route or a
  runtime variable.
- [../docs/consent-flow.md](../docs/consent-flow.md) (Dutch), "Toestemming
  verlenen" and "Demo shortcuts": the whole flow, every refusal and the shared
  session cookie; open before changing start or return.
- [ictu-gbo tenant](https://gitlab.com/datastelsel.nl/federatief/simulation/tenants/ictu-gbo):
  the simulation deployment of this image.
