# keyper-mock

## Purpose

The Installatie Register (Keyper) portal of the LVG/IR pilot (port 9004): the
installer's request, the owner's approval with an ownership check at LVG, and
the installer's data behind a check that runs again on every fetch. IR itself is
mock: its state (the request, the decisions, the consent's id) lives in
`sessionStorage` (`lib/aanvraag.ts`), and so does the second consent (citizen to
installer), which is not GBO's. The ownership check is the real chain, through
`ir-backend` (`dienstverlener-backend` with `QUERY_KIND=lvg`) at `/api`.

## Contracts

- No consent token or code in the browser: IR's state holds only `consentId`,
  the ownership check sends only `vbo_id` and `ir-backend` uses its session's
  token. `tests/consentFlow.test.mjs` checks the sources.
- The installer's recurring check works only because installer and owner share
  one browser, and so `ir-backend`'s session cookie.
- `/verzoek/terug` must equal `ir-backend`'s `CONSENT_REDIRECT_URI` and the
  return location registered in
  `services/dvtp-onboarding-register/config/demo.json`. `PortalTerug` clears the
  code and completes once, as in the dienstverlener-mock;
  `src/lib/consentFlow.ts` is a copy of that app's; change both.
- `ownershipConsentRequest` asks `lvg:vbo:eigendom`, the scope rule LVG0001
  pins.
- `outcomeOf`: only the requested VBO-id is "owner", only `null` without errors
  is "not owner"; anything else is "unconfirmed" and IR releases nothing.
- The VBO-ids in `BUILDINGS` must exist in
  `services/lvg-graphql-server/mockdata/owners.json`.
- Runtime configuration is only `CONSENT_PORTAL_URL`, for the "Mijn
  toestemmingen" link (`portalBase`); a value still `${VAR}` counts as unset,
  and the Dockerfile declares every template variable empty
  (`tests/runtimeConfig.test.mjs`). Compose passes `VITE_CONSENT_PORTAL_URL`.

## Verify

`npx tsc --noEmit && npm test` (what CI runs). End to end: `make demo`, start at
`:9004` with BSN 123456789 (owner) and 987654321 (not the owner).

## Decisions

- IR state mocked in the browser, ownership check through the real chain:
  [#435](https://github.com/ICTU/GBO-demo/pull/435).
- Pushed request and one-time code; IR keeps the consent's id, not the token:
  [#693](https://github.com/ICTU/GBO-demo/pull/693).
- Every runtime-config variable declared in the image:
  [#657](https://github.com/ICTU/GBO-demo/pull/657).
- `.npmrc` installs no npm release younger than seven days:
  [#197](https://github.com/ICTU/GBO-demo/pull/197).

## Open questions

- One consent lets IR ask about any VBO-id; whether the VBO-id belongs in the
  consent is a question for the pilot
  ([#435](https://github.com/ICTU/GBO-demo/pull/435)).

## Links

- [../docs/lvg-ir.md](../docs/lvg-ir.md) (Dutch): the two consents, the
  components and the three outcomes; open before changing the flow.
- [../docs/consent-flow.md](../docs/consent-flow.md) (Dutch): the consent flow
  and its refusals.
