# GBO-demo AppHost

`apphost.cs` is a C# file-based [Aspire](https://aspire.dev) 17 AppHost. It
runs the whole demo: the Go services, the FSC network, OpenFTV, the EUDI and
ASI parts, the frontends and the run-once steps between them, in dependency
order. It replaced the compose files and the Makefile; `PLAN.md` records the
migration and the follow-ups.

## Prerequisites

- Aspire CLI 17 (`brew install --cask aspire`, or the install script from aspire.dev)
- .NET 10 SDK
- Docker (Docker Desktop, OrbStack or Docker Engine)
- Node.js 24 or newer with npm 11, for the Vite frontends (`npm ci`). It need
  not be first on PATH: the AppHost finds one in PATH, Homebrew, nvm, Volta or
  fnm, or takes `Gbo:NodeDir`
- Go 1.26 or newer, for the host-side certificate steps
- Bash, `curl`, `jq`, OpenSSL; Python 3 for the OpenFTV Manager seed

## Run

From this directory:

```bash
aspire run                          # core only
aspire run --launch-profile dvtp    # the DvTP consent demo (was: make demo)
aspire run --launch-profile full    # DvTP and EUDI (was: make demo-full)
```

The dashboard opens at <http://localhost:15126>. Stop with Ctrl+C.
`aspire start` runs the same in the background, and `aspire stop` stops it.

### Profiles

A profile picks which parts run, as the compose profiles did. Set it with a
launch profile from `apphost.run.json` or with `Gbo__Profiles`,
comma-separated:

| Profile | Adds | Was |
| --- | --- | --- |
| (none) | sources, PDP, logbooks, developer portal, landing page | `make demo-minimal` |
| `dvtp` | the FSC network, consumers, consent portal, LVG and the Installatie Register | `make demo` |
| `eudi` | the Source Registry, source onboarding, the issuance server and the EUDI adapter | `make demo-eudi` |
| `full` | `dvtp` + `eudi`, and the separate BRP FSC peer | `make demo-full` |
| `onboarding` | the Source Registry and the reconciler only | `--profile onboarding` |
| `integrator` | the integrator's FSC peer, seed and backend | `make demo-integrator` |
| `asi` | the ASIP's FSC peer, authorization server, discovery stub, reference QTSP and the wallet verifiers once provisioned | `make demo-asi` |
| `manager` | Keycloak, the OpenFTV Manager and UI, and the policy seed | `make demo-manager` |
| `credential-issuer` | the own credential issuer, trust host and test PID source | `make credential-issuer-up` |

For example: `Gbo__Profiles=dvtp,integrator aspire run`.

### Settings

Settings are configuration keys. Pass them after `--`
(`aspire run -- --Gbo:PublicHost=demo.example.org`) or as environment
variables (`Gbo__PublicHost=demo.example.org`).

| Setting | Default | Purpose |
| --- | --- | --- |
| `Gbo:PublicHost`, `Gbo:PublicScheme` | `localhost`, `http` | Host and scheme of the browser-facing links (was `GBO_PUBLIC_HOST`, `GBO_PUBLIC_SCHEME`) |
| `Gbo:ExtraAllowedHosts` | | More host names the Vite dev servers accept |
| `Gbo:SecretsDir` | `../.local/secrets` | Pre-provisioned development CAs and the leaves signed from them (was `ONBOARDING_SECRETS_DIR`) |
| `Gbo:PublicCertificatesDir`, `Gbo:TrustPublishDir` | `../.local/public-certificates`, `../.local/public-trust` | Public certificate projection; files the trust host serves |
| `Gbo:CloudflareTunnel` | `false` | Start cloudflared for the public hostnames (was the tunnel overlay) |
| `Gbo:IssuerPublicUrl` | | Own credential issuer's public URL, for the portal and the ASI issuance resources |
| `Gbo:AsiAuthorizationServerPublicUrl`, `Gbo:AsiProviderPublicUrl`, `Gbo:QtspPublicUrl` | public host on 9425, 9424, 9427 | Public base URLs of the ASI route |
| `Gbo:FtvManagerUrl`, `Gbo:FtvOidcAuthority` | public host on 9280, 9284 | Browser URLs of the OpenFTV Manager and its IdP |
| `Gbo:EudiUlBase`, `Gbo:VersionString`, `Gbo:Issuer*`, `Gbo:Pid*`, `Gbo:WalletRootsDir`, `Gbo:QtspIssuerOffersUrl`, `Gbo:AsiIssuanceResources`, `Gbo:AsiRequireAuthorizationDetails` | as before | The remaining former `.env` settings, one to one |

### Secrets and parameters

Values the AppHost needs at run time are parameters, kept in the AppHost
user secrets (`aspire secret list`, `aspire secret set <key> <value>`):

- **Generated on first run:** the database and Keycloak passwords:
  `ftv-postgres-password`, `ftv-adl-reader-password`, `fsc-postgres-password`,
  `eudi-postgres-password`, `source-registry-password`,
  `source-registry-reader-password`, `credential-issuer-db-password`,
  `keycloak-admin-password`, `ftv-manager-auditor-password` and
  `ftv-manager-deploy-password`.
- **Asked for when the profile needs them**, in the dashboard or with
  `aspire secret set Parameters:<name> <value>`: `eudi-public-url`,
  `eudi-bri-url`, `issuer-public-url`, `eudi-trust-public-url` and
  `cloudflare-tunnel-token`.
- **Local demo defaults** (set real values in a shared environment):
  `ldv-write-token`, `ldv-read-token`, the `ldv-subject-pseudonym-key*`
  keys, `subject-ref-key` and `bsn-authorised-oins`.
- **Dashboard keys:** `AppHost:DashboardApiKey` and `AppHost:OtlpApiKey` are
  random per run unless set. dev-portal-backend and the collector are given
  them.

### Fixed ports

Most ports are assigned per run and listed in the dashboard. Fixed are the
ones a URL, a certificate or the consent redirect (consumer mock port + 1)
depends on:

| Ports | What |
| --- | --- |
| 9000–9004 | the landing page, consumer mock, consent portal, developer portal and Installatie Register |
| 8091–8102 | certportal, the directory UI and the FSC Controllers; the EDI Outway on 8087 |
| 9409, 9424–9432, 18001 | the EUDI adapter, the ASI route, the credential issuer, trust host and issuance server |
| 9280–9284 | the OpenFTV Manager, its UI and Keycloak |
| 15126 | the dashboard |

## What runs where

- **Go services:** containers built from their own Dockerfiles, with the
  hostnames, container ports and mounts they had under compose. The FSC
  contracts, the PIP pull config and the collector address them by name.
- **Frontends:** Vite dev servers on the host (`AddViteApp`), installed with
  `npm ci`.
- **Host steps** (`AddExecutable`): the pki-tools image build, the policy
  check (`scripts/policy-check.sh`, before the PDP starts), the FSC
  certificates (`scripts/fsc-pki.sh`), the development certificates of the
  demo sources and the ASI and QTSP certificates (`go run` in the services,
  so the CA keys never enter a container), the nl-wallet image builds
  (`scripts/nlwallet-image.sh`) and the Manager seed.
- **Run-once containers:** migrations, database bootstraps, materialisation
  and the FSC contract seeds. Restart one from the dashboard to rerun it.

Every container carries `com.docker.compose.project=gbo-demo-aspire` and
`com.docker.compose.service=<resource>`, so Docker Desktop and OrbStack group
them.

Sizes measured on macOS (arm64):

| Run | Containers (long-running / run-once) | Host processes | Healthy after |
| --- | --- | --- | --- |
| core | 23 (21 / 2) | 7 | 35 s warm |
| `dvtp` | 86 (58 / 28) | 21 | 63 s warm, about 800 MiB |
| every profile (`aspire publish`) | about 139 | | not run |

## Scripts

| Script | Does | Was |
| --- | --- | --- |
| `../scripts/policy-check.sh [--test]` | Rego structure check and `opa check`; with `--test` also `opa fmt --diff` and `opa test` | `make policy-check`, `make policy-test` |
| `scripts/provision.sh <step>` | One-off host provisioning: trust lists, registrar root, wallet registration, test PID, QTSP registration, ASI wallet verifiers | the `provision-*`, `publish-trust-lists` and `create-development-registrar-root` targets |
| `scripts/fsc-clean.sh` | Wipes the FSC certificates and contracts | `make fsc-clean` |
| `scripts/fsc-pki.sh`, `scripts/nlwallet-image.sh` | Used by the AppHost | `make fsc-*-certs`, `make eudi-images` |

## Telemetry

The services export to the `otel-collector` container (`otel-collector.yaml`),
which forwards to the dashboard and to dev-portal-backend's live strip. The
PDP's console, its decision log included, reaches the dashboard as OTLP logs
through `logtee` in the PDP image. The developer portal reads traces and the
decision log back through the dashboard's telemetry API
(`TELEMETRY_API_URL`). The dashboard keeps one store across restarts
(`ASPIRE_DASHBOARD_PERSISTENCE_MODE=Resume` in `apphost.run.json`). See
`../OBSERVABILITY.md` for the simulation deployment, which keeps Jaeger and
Loki.

The default launch profiles use plain http, since the collector forwards to
the dashboard over plaintext OTLP.

## Publishing

`aspire publish` writes `aspire-output/docker-compose.yaml` for running the
model without Aspire. It refers to the images through variables and leaves
out the host steps, so it is a starting point rather than a replacement yet
(PLAN.md, follow-ups). The Helm chart in `deploy/helm/gbo-app` is unchanged
and stays what the simulation deployment uses.

## Phase 0 spike results (2026-11-18)

The spike that preceded the migration checked three things:

- **Persistence:** with `ASPIRE_DASHBOARD_PERSISTENCE_MODE=Resume`, the
  telemetry API returned the same traces after a full stop and start. In the
  default `Run` mode it returned none.
- **Fixed API key:** the AppHost overwrites `DASHBOARD__API__PRIMARYAPIKEY`,
  but takes `AppHost:DashboardApiKey`.
- **OTLP endpoint:** the Go services' `otlptracegrpc.WithEndpoint` +
  `WithInsecure` setup could not take the URL Aspire injects ("too many
  colons in address"). That is fixed in the services.
