# Aspire phase 0 spike

A C# file-based Aspire 17 AppHost (`apphost.cs`) that runs two GBO-demo Go
services. It is a spike: it checks that the approach works and records what
breaks. It does not replace `docker-compose.yml` or the Makefile.

| Resource | How it runs | Notes |
| --- | --- | --- |
| `logboek-bd` | `AddGoApp` (`Aspire.Hosting.Go`), `go run .` in `services/ldv-logboek` on the host | SQLite at `.aspire-data/logboek-bd/logboek.db` (git-ignored), register `services/ldv-logboek/config/verwerkingsactiviteiten-bd.json`, health `/health` |
| `graphql-server` | `AddDockerfile` with the repository root as context and `services/graphql-server/Dockerfile` | `config/` bind-mounted read-only, outbox volume `gbo-spike-ldv-outbox-graphql-server`, `LDV_LOGBOOK_URL` from the `logboek-bd` endpoint, `WaitFor(logboek-bd)` |

The LDV tokens and the pseudonym key are secret `AddParameter`s that default to
the same local-demo values as `docker-compose.yml`.

## Prerequisites

- Aspire CLI 17.0.0 (`brew upgrade --cask aspire`, or the install script from aspire.dev)
- .NET 10 SDK (tested with 10.0.400)
- Go 1.26 or later on PATH (tested with 1.27.4)
- Docker (tested with Engine 29.8.2)

## Run

From this directory:

```bash
export AppHost__DashboardApiKey="$(openssl rand -hex 24)"   # fixed key for the telemetry API
export GboSpike__OtlpHostPortWorkaround=true                 # see "OTLP endpoint" below
aspire run --launch-profile http                             # or: aspire start --launch-profile http
```

Then:

```bash
curl -s -H "x-api-key: $AppHost__DashboardApiKey" http://localhost:15126/api/telemetry/resources
curl -s -H "x-api-key: $AppHost__DashboardApiKey" http://localhost:15126/api/telemetry/traces
```

Stop with Ctrl+C, or with `aspire stop` after `aspire start`.

The service ports are random on every start (`aspire describe` lists them).
Fixing them comes later, through `WithHttpEndpoint(port: …)`.

## Dashboard settings

These live in the `environmentVariables` of both launch profiles in
`apphost.run.json`. The C# template generates that file; it plays the same
role as the `profiles` section in `aspire.config.json`.

- `ASPIRE_DASHBOARD_PERSISTENCE_MODE=Resume`
- `DASHBOARD__API__AUTHMODE=ApiKey`

The API key is not in the repository. Setting `DASHBOARD__API__PRIMARYAPIKEY`
in the shell has no effect: the AppHost overwrites it with a generated key
(the API answered 401). The AppHost does read `AppHost:DashboardApiKey`, so
`AppHost__DashboardApiKey` in the environment, or
`aspire secret set AppHost:DashboardApiKey <value>`, gives a fixed key. The
telemetry API expects it in the `x-api-key` header.

## OTLP endpoint: does not work unchanged

The Go services build their exporter like this:

```go
otlptracegrpc.New(ctx,
    otlptracegrpc.WithEndpoint(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
    otlptracegrpc.WithInsecure())
```

`WithEndpoint` expects `host:port`. Aspire injects a URL, for a container
`https://aspire.dev.internal:21134`, together with `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`
and the API key in `OTEL_EXPORTER_OTLP_HEADERS`. Without changes no spans
arrive, and the service logs:

```text
traces export: exporter export timeout: rpc error: code = Unavailable desc = invalid target address https://aspire.dev.internal:21134, error info: address https://aspire.dev.internal:21134:443: too many colons in address
```

Under the https profile, `WithInsecure()` would also fail against the TLS
OTLP port.

Workaround in this spike, with no change to service code: the http launch
profile (plaintext OTLP on 19025), plus `GboSpike:OtlpHostPortWorkaround=true`.
That overrides the variable with `aspire.dev.internal:19025`. Rewriting the
injected value inside a `WithEnvironment` callback did not work, because at
that point it is not a plain string.

The proper fix belongs in the services. Drop `WithEndpoint` and
`WithInsecure`, and let the OTel SDK read `OTEL_EXPORTER_OTLP_ENDPOINT` and
`OTEL_EXPORTER_OTLP_HEADERS` itself. The SDK accepts a URL with a scheme and
uses TLS for `https`. That needs a change in the 10 modules that
initialise their exporter this way (asi-provider, bron-sidecar,
brp-graphql-server, bsnk-mock, consent-portal-backend, consent-register,
dienstverlener-backend, eudi-adapter, graphql-server, lvg-graphql-server),
and compose has to set
`http://otel-collector:4317` instead of `otel-collector:4317`.

`ldv-logboek` has no OpenTelemetry instrumentation at all, so `logboek-bd`
sends no traces. Its structured logs show up as console logs only.

## Results (2026-11-18, macOS arm64)

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
