# Aspire migration: plan and checklist

Status: the pull request stays a draft until the Go AppHost is generally
available, or until the Go AppHost issues that matter for this repository
are solved (see "Go AppHost" under Follow-ups).

Branch `aspire/migration`, local only until reviewed. The full plan, with
background and risks, is the "GBO-demo: Aspirify migration plan" doc. This
file is the map for reviewing the commits: each phase lists its steps, and a
step is ticked in the commit that completes it.

Decisions already taken:

- C# file-based AppHost (`aspire/apphost.cs`), Aspire 17. Port to a Go
  AppHost when that is generally available. Done ahead of GA on branch
  `aspire/go-apphost` (`aspire/apphost.go`); `GO-APPHOST.md` lists what the
  preview lacks and the workarounds.
- The Helm chart (`deploy/helm/gbo-app`) and its example values stay as they
  are.

## Phase 0: spike (done)

- [x] C# AppHost with two Go services, dashboard Resume persistence and a
  fixed telemetry API key (`aspire/README.md`).

## Phase 1: core demo under Aspire, compose still working

- [x] OTLP exporter init in the 10 Go modules: drop
  `otlptracegrpc.WithEndpoint` and `WithInsecure`, so the SDK reads
  `OTEL_EXPORTER_OTLP_*`. Compose sets `http://otel-collector:4317`.
- [x] OpenTelemetry tracing in `ldv-logboek`.
- [x] demo-minimal core in `apphost.cs`:
  - postgres-ftv and its init step, with pinned image tags
  - openftv-pdp
  - consent-register and dvtp-onboarding-register
  - the 7 logboeken, from one loop
  - graphql servers and sidecars
  - bsnk-mock and decryption-component
  - dev-portal-backend, and developer-portal through `AddViteApp`
  - the OTel collector, fanning out to the dashboard and dev-portal-backend
  - Jaeger, for now
  - secrets as parameters
- [x] The developer portal's core works under `aspire run`: the portal, the source proxies, PDP health, and spans in the dashboard, Jaeger and the live strip. The DvTP scenario needs FSC (phase 2).

## Phase 2: full parity, then remove compose and make

- [x] FSC infrastructure through an `AddFscPeer` helper.
- [x] PKI bootstrap as run-once resources.
- [x] Host `provision-*` steps as `AddExecutable`, so CA keys never enter containers.
- [x] Seeds and migrations as run-once resources with `WaitForCompletion`.
- [x] policy-check as a gate the PDP waits for.
- [x] Compose profiles as parameters: dvtp, eudi, asi, integrator, manager,
  onboarding, credential-issuer, wallet-registration.
- [x] nl-wallet Rust images through `AddDockerfile`.
- [x] cloudflared tunnel behind a parameter.
- [x] The remaining frontends through `AddViteApp`.
- [x] Fixed ports where public URLs, certificates or the DV mock + 1 rule need them.
- [x] `AddDockerComposeEnvironment` for `aspire publish`.
- [x] demo-full parity, as far as it can be checked locally: dvtp verified end to end; EUDI and ASI modelled and published, not run (no development CAs, public URLs or nl-wallet build here).
- [x] Delete:
  - `docker-compose*.yml`, the `Makefile` and the `GBO_PORT_*` plumbing
  - `scripts/update-env.py`, `scripts/wait-openftv-admission.sh` and `certs/generate.sh`
  - dead references to any of these

## Phase 3: observability on the dashboard

- [x] dev-portal-backend and developer-portal read traces from the dashboard
  telemetry API. This needs a fixed API key through `AppHost:DashboardApiKey`
  and Resume persistence.
- [x] The OPA decision log arrives as OTLP logs instead of being read from
  Loki: the PDP image runs the PDP under `logtee`, which exports its console
  as OTLP log records. Not done: an slog OTel bridge in the Go services; their
  console logs already show in the dashboard (follow-up below).
- [x] Local Jaeger, Loki, Grafana and Promtail are gone from the AppHost.

## Docs

- [x] Run sections of README, TROUBLESHOOTING, AGENTS.md, OBSERVABILITY.md and `docs/*.md`.
- [x] Script comments that say `make …`, and the `.github` PR and bug templates.
- [x] CHANGELOG entry.

## Follow-ups, after review

- Fewer containers (decided 2026-11-18: not now, revisit after review):
  - one shared Postgres for FTV, EUDI and FSC
  - the logboeken and mocks as host processes (`AddGoApp`)
- An slog OTel bridge in the Go services, so their logs are structured
  OTLP logs with trace correlation in the dashboard.
- `aspire publish`: the generated compose refers to images by variable and
  leaves out the host steps (PKI, provisioning); a push step and a decision
  on the host steps make it a standalone replacement.
- Go AppHost: ported on `aspire/go-apphost` (`apphost.go`). Remove the
  `TODO(go-apphost/...)` workarounds as Aspire fixes them (GO-APPHOST.md);
  the pull request stays a draft until the Go AppHost is GA or those gaps
  are closed.
