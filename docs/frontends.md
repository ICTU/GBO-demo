# Frontends: serving, runtime configuration and npm policy

What the five frontends (`developer-portal`, `landing-page`, `keyper-mock`,
`dienstverlener-mock`, `toestemmingsportaal-frontend`) share. Each frontend's
own `AGENTS.md` holds only its own routes and variables; open this doc before
adding a proxy route or a runtime variable to any of them.

## Two ways of serving

- **Compose** builds the image's `build` stage and runs the Vite dev server in
  it (`npm run dev`), with `src/` mounted; proxy targets come from the Compose
  environment (`*_TARGET`, read in `vite.config.ts`).
- **The published image** (GHCR, built by `.github/workflows/images.yml`; the
  simulation deployment runs it) serves the built files with nginx
  (`nginx.conf`), proxying to fixed Compose service names.

A new proxy route goes in both `vite.config.ts` and `nginx.conf`, or it works in
one and not the other.

## Runtime configuration

Browser-side URLs only: published host ports and `GBO_PUBLIC_HOST`, never
Compose service names, and never a secret (everything ends up in the browser).
Two styles exist:

| Style             | Frontends                     | How                                                                                                                                                                                                                                                                                                                                            |
| ----------------- | ----------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| envsubst template | `landing-page`, `keyper-mock` | nginx's envsubst fills `runtime-config.js.template` at container start. envsubst leaves an unset `${VAR}` literally, so the `Dockerfile` declares every template variable empty, and the reader treats a value still `${VAR}` as unset ([#126](https://github.com/ICTU/GBO-demo/pull/126), [#657](https://github.com/ICTU/GBO-demo/pull/657)). |
| entrypoint script | `developer-portal`            | `docker-entrypoint.d/40-runtime-config.sh` writes `window.__GBO_RUNTIME_CONFIG__` as JSON at container start.                                                                                                                                                                                                                                  |

`dienstverlener-mock` and `toestemmingsportaal-frontend` have no runtime
configuration. In every style the code reads the runtime value first, then
`import.meta.env.VITE_*` (what Compose passes to the dev server), then a
localhost default.

## Build and dependencies

- The Node build stage runs on `$BUILDPLATFORM`; only the nginx stage is per
  target platform.
- `.npmrc` sets `min-release-age=7`: npm installs no release younger than seven
  days ([#197](https://github.com/ICTU/GBO-demo/pull/197)).
- The frontends share no package: code used by two of them (such as
  `src/lib/consentFlow.ts` in both mocks) is a copy, and each image builds from
  its own directory.
