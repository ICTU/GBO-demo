# landing-page

## Purpose

The demo's public front door (port 9000): what GBO is, the streams, and "Zelf
uitproberen", which starts the live flows (the DvTP scenarios Hypotheek-BV and
Installatie Register, the EUDI wallet QR) and points to the developer portal and
the infrastructure. Static, no backend of its own; its one proxied path is
`/eudi-offers.json`, to `eudi-adapter`. Compose starts it without a profile.

## Contracts

- Every link to another service comes from `links` in `src/config.ts`:
  `window.__GBO_RUNTIME_CONFIG__` (envsubst of `runtime-config.js.template` at
  container start), then `VITE_*`, then a localhost default; a value still
  `${VAR}` counts as unset. These are browser-side URLs: published host ports
  and `GBO_PUBLIC_HOST`, never Compose service names.
- The wallet link is built client-side (`walletUniversalLink` in `src/eudi.ts`),
  deliberately duplicated in the developer portal's `EudiForm.tsx`; change both.
  Its `client_id` is derived from `EUDI_PUBLIC_URL` because the issuance server
  derives it the same way; without that URL the page shows why there is no QR.
- The offers come from the active source release (`eudi-adapter`'s
  `/eudi-offers.json`); an offer's `key` is a disclosure usecase of the issuance
  server. In the image `15-configure-eudi-offers.sh` writes that location at
  start from `EUDI_API_TARGET` (an origin, no path); unset, it answers `503`.
- `/healthz` answers the Helm chart's default probe
  (`deploy/helm/gbo-app/values.yaml`).
- The test BSNs in `TryItOut`'s Installatie Register hint must match
  `services/lvg-graphql-server/mockdata/owners.json`.

## Patterns

A new outgoing link: a field in `runtime-config.js.template`, the same variable
declared empty in the `Dockerfile`, a `VITE_*` variable in `docker-compose.yml`
and an entry with its default in `links`.

## Pitfalls

- envsubst leaves an unset `${VAR}` literally, which became a broken href; hence
  the empty declarations and the check in `clean`
  ([#126](https://github.com/ICTU/GBO-demo/pull/126)).
- The FSC controller links pointed at the directory peer and ignored the
  `FSC_PORT_*` overrides; they follow `fsc-infra/docker-compose.yml`'s ports
  ([#461](https://github.com/ICTU/GBO-demo/pull/461)).

## Verify

`npx tsc --noEmit && npm run build` (no unit tests). For the image: build it and
read `/usr/share/nginx/html/runtime-config.js` with and without the variables
set.

## Decisions

- Try-it-out built around the flows that run, links resolved at runtime:
  [#126](https://github.com/ICTU/GBO-demo/pull/126).
- One wallet QR with a choice of credential:
  [#138](https://github.com/ICTU/GBO-demo/pull/138).
- Installatie Register as a scenario in the DvTP card, not a stream:
  [#628](https://github.com/ICTU/GBO-demo/pull/628).
- `.npmrc` installs no npm release younger than seven days:
  [#197](https://github.com/ICTU/GBO-demo/pull/197).
