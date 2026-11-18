# GBO-demo AppHost

`apphost.cs` is a C# file-based Aspire 17 AppHost. It is replacing
`docker-compose.yml`, `fsc-infra/docker-compose.yml` and the Makefile; see
`PLAN.md` for what it covers so far.

## Prerequisites

- Aspire CLI 17 (`brew install --cask aspire`, or the install script from aspire.dev)
- .NET 10 SDK
- Docker
- Node.js and npm, for the Vite frontends

## First run

The required secrets have no default. Set them once:

```bash
aspire secret set Parameters:ftv-postgres-password "$(openssl rand -hex 16)"
aspire secret set Parameters:ftv-adl-reader-password "$(openssl rand -hex 16)"
aspire secret set AppHost:DashboardApiKey "$(openssl rand -hex 24)"   # optional: a fixed telemetry API key
```

`aspire secret set` stores them in the AppHost user secrets, outside the
repository. Values left unset are prompted for in the dashboard.

## Run

```bash
aspire run
```

The dashboard opens at http://localhost:15126 and lists every resource with
its URLs. The default launch profile is `http`: the collector forwards to the
dashboard over plaintext OTLP.

## How telemetry flows

The services export spans to the `otel-collector` container, as they do
under compose. `otel-collector.yaml` fans them out to the dashboard, to
Jaeger (the developer portal still queries it) and to dev-portal-backend's
live strip.

The dashboard keeps one store across restarts (`ASPIRE_DASHBOARD_PERSISTENCE_MODE=Resume`
in `apphost.run.json`).

## Phase 0 spike results (2026-11-18, macOS arm64)

Time from `aspire start` until both resources are Healthy:

- 48 s for the first start, which builds the graphql-server image
- 13 to 18 s for later starts

Traffic: five rounds of `GET /health` and `POST /graphql` on graphql-server,
and `GET /health` and `GET /verwerkingsactiviteiten` on logboek-bd. All
returned 200. The GraphQL calls wrote LDV records to logboek-bd.

| Step | Persistence | Query after | Result |
| --- | --- | --- | --- |
| A | Resume | traffic | 5 traces, from graphql-server only (`360eb236…`, `b806070d…`, `d51316ff…`, `f0e2bce0…`, `fe1d79b2…`) |
| B | Resume | stop, start, no traffic | the same 5 trace IDs in `/api/telemetry/traces`; the Traces page in the UI shows 5 traces too |
| C | Run (default) | traffic | 5 new traces |
| D | Run (default) | stop, start, no traffic | 0 traces, `resources: []` |

Where the data lives:

- Resume: `~/.aspire/dashboard/resumes/aspire-ec5ba455d893c7b1/dashboard.db`, with a `.lock` beside it while running.
- Run: a new `~/.aspire/dashboard/runs/<UTC timestamp>/` per start. The previous runs are kept for the UI history selector, up to 10.

Other notes:

- `aspire new aspire-empty --language csharp` generates `#:package` lines for 17.0.0.
- Every build gives a NuGet warning, NU1903, for `Microsoft.DotNet.ILCompiler` 10.0.11 (GHSA-p3jp-9v83-76w9), which comes in through the AppHost.
