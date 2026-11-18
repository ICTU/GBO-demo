# Aspire migration: plan and checklist

Branch `aspire/migration`, local only until reviewed. The full plan, with
background and risks, is the "GBO-demo: Aspirify migration plan" doc. This
file is the map for reviewing the commits: each phase lists its steps, and a
step is ticked in the commit that completes it.

Decisions already taken:

- C# file-based AppHost (`aspire/apphost.cs`), Aspire 17. Port to a Go
  AppHost when that is generally available.
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

- [ ] FSC infrastructure through an `AddFscPeer` helper.
- [ ] PKI bootstrap as run-once resources.
- [ ] Host `provision-*` steps as `AddExecutable`, so CA keys never enter containers.
- [ ] Seeds and migrations as run-once resources with `WaitForCompletion`.
- [ ] policy-check as a gate the PDP waits for.
- [ ] Compose profiles as parameters: dvtp, eudi, asi, integrator, manager,
  onboarding, credential-issuer, wallet-registration.
- [ ] nl-wallet Rust images through `AddDockerfile`.
- [ ] cloudflared tunnel behind a parameter.
- [ ] The remaining frontends through `AddViteApp`.
- [ ] Fixed ports where public URLs, certificates or the DV mock + 1 rule need them.
- [ ] `AddDockerComposeEnvironment` for `aspire publish`.
- [ ] demo-full parity, as far as it can be checked locally.
- [ ] Delete:
  - `docker-compose*.yml`, the `Makefile` and the `GBO_PORT_*` plumbing
  - `scripts/update-env.py`, `scripts/wait-openftv-admission.sh` and `certs/generate.sh`
  - dead references to any of these

## Phase 3: observability on the dashboard

- [ ] dev-portal-backend and developer-portal read traces from the dashboard
  telemetry API. This needs a fixed API key through `AppHost:DashboardApiKey`
  and Resume persistence.
- [ ] The OPA decision log arrives as OTLP logs instead of being read from
  Loki. This also adds the slog OTel bridge to the Go services.
- [ ] Local Jaeger, Loki, Grafana and Promtail are gone from the AppHost.

## Docs

- [ ] Run sections of README, TROUBLESHOOTING, AGENTS.md, OBSERVABILITY.md and `docs/*.md`.
- [ ] Script comments that say `make …`, and the `.github` PR and bug templates.
- [ ] CHANGELOG entry.
