# The Go AppHost: status

`apphost.go` replaces the C# file-based `apphost.cs` with a Go AppHost on the
Aspire 17.0 Go preview. This file records what works, what the preview does not
do yet, the workarounds in the code, and what cannot be worked around. Each
workaround is marked `TODO(go-apphost/<gap>)` in the code; remove it when the
upstream fix ships.

Checked with Aspire CLI 17.0.0, Go 1.27.4 on macOS (arm64), 2026-11-20.

## What works

- **The whole app model.** Every resource, profile, wait, mount, endpoint,
  parameter and setting of the C# AppHost is in `apphost.go`. Until the
  PostgreSQL switch below (2026-11-23), `aspire publish` wrote the same
  `docker-compose.yaml` and `.env` keys as the C# AppHost (default, `core`
  and every profile at once, 170 services). The C# AppHost is no longer the
  reference: the Go AppHost uses Aspire features the C# one did not.
- **`aspire run` of the default (dvtp) demo**, on top of the volumes the C#
  AppHost made: every resource comes up healthy or exits 0 (2026-11-23, with
  the PostgreSQL integration: 141 resources, 39 setup steps hidden after
  success, 90 resources left in the list). The DvTP flow
  works end to end in the browser: consumer mock, DigiD mock login, consent in
  MijnOverheid, back to the consumer with both income years fetched over FSC
  through the PDP. A restart on the same volumes comes up the same way.
- **Telemetry.** The collector forwards with the OTLP key the AppHost writes
  into the configuration; the dashboard telemetry API returns traces and the
  PDP's decision log (what the developer portal reads), for the current run
  (Run persistence; earlier runs in the Recordings drawer).
- **Settings** after `--` (`aspire run -- --Gbo:PublicHost=...`) reach the
  AppHost, as with C#.
- **No .NET SDK.** The Aspire CLI runs a prebuilt AppHost server; the only
  toolchain the AppHost needs is Go.
- **The launch profile** lives in `aspire.config.json` instead of
  `apphost.run.json`, with the same environment apart from persistence
  (dashboard API key auth; Run persistence since 2026-11-23). Which demo profile runs is `Gbo__Profiles`
  (`Gbo__Profiles=core aspire run`), checked for `core` at run time.
- **Configuration** (`Gbo:*` settings, `Gbo__Profiles`, user secrets,
  `aspire secret set`) reads through `GetConfiguration()`, and the AppHost can
  write the dashboard keys into it, as C# did.
- **CI**: the OpenFTV version check and the CI planner read `apphost.go`.

## Aspire features used beyond the C# AppHost (2026-11-23)

- **Aspire's PostgreSQL integration** (`Aspire.Hosting.PostgreSQL`) runs the
  three Postgres servers (`postgres-ftv`, `fsc-postgres`, `postgres-eudi`):
  health checks instead of `pg_isready` loops, credentials from the existing
  parameters, and the existing data volumes. Each database is an
  `AddDatabase` resource that Aspire creates when it is missing and that its
  users wait for. That replaced the `fsc-databases` step, the Postgres
  `POSTGRES_DB` variables and `services/openftv-manager/initdb`.
- **Roles stay a run-once step.** `postgres-ftv-init` (ADL reader),
  `source-registry-bootstrap` and `credential-issuer-db-bootstrap` still run,
  slimmer, after their database exists. Aspire's creation script cannot take
  them over: it runs as one batch on the server, cannot switch to the new
  database (`\c`), and `CREATE DATABASE` refuses to share a batch with other
  statements ("cannot be executed within a pipeline", tested). The two
  bootstrap scripts now also set the database owner, since Aspire creates the
  database before they run (tested on a fresh database: the owner role can
  create its schema).
- **`WithHiddenOnCompletion`** on every setup step: it shows while it runs or
  when it fails and leaves the list once it succeeds. `manager-seed` and the
  two `*-materialize` steps stay visible, since they are rerun by hand.
- **Trade-off in `aspire publish`:** Aspire's Docker Compose output does not
  create `AddDatabase` databases (no `CREATE DATABASE`, no `POSTGRES_DB`), so
  the generated compose file no longer sets the databases up. It was not a
  standalone replacement before either (PLAN.md, follow-ups).
- **Dashboard tags and order** (`WithTags`, `WithDisplayOrder`): own build,
  off the shelf, monitoring, setup.
- **`WithOtlpExporter`** on every service that exports telemetry (and the
  OpenFTV PDP's logtee), with the endpoint set back to the collector so the
  developer portal's live strip keeps its feed. Aspire adds the service name
  and the `service.instance.id` that ties traces and logs to the resource, so
  the dashboard no longer lists them as separate telemetry-only sources.
  This needs Run persistence: with Resume, the previous runs' instance ids
  matched no resource, piled up as telemetry sources and made the base-name
  lookup the developer portal uses for the PDP log (`resource=openftv-pdp`)
  return 404. With Run persistence the dashboard shows only the current run
  (Recordings drawer for the last 10) and the lookup works.

## Gaps in the preview, with a workaround

| Gap (`TODO` tag) | What happens | Workaround | Upstream |
| --- | --- | --- | --- |
| `nil-args` | `AddExecutable(name, cmd, dir, nil)` leaves `args` out of the request; the server rejects it with "Missing required argument 'args'". | Pass an empty slice (`model.executable`). | [microsoft/aspire#22569](https://github.com/microsoft/aspire/issues/22569) |
| `parameter-arg` | A `ParameterResource` handle added through `WithArgsCallback` runs, but breaks `aspire publish` with "Command line args must be strings". C# passes `parameter.Resource`, which the Go SDK has no counterpart for. | Wrap it: `RefExpr("%v", p)` publishes as `${NAME}` (`model.argValue`). | [microsoft/aspire#22570](https://github.com/microsoft/aspire/issues/22570) |
| `string-args` | `WithArgs` takes `[]string`; C# takes `object[]` with parameters and expressions mixed in. | Mixed arguments go through `WithArgsCallback` (`model.containerArgs`, `model.executableArgs`). | Not filed: by design upstream; [microsoft/aspire#22570](https://github.com/microsoft/aspire/issues/22570) covers the part that breaks |
| `typed-model` | No typed `builder.Resources`; `DistributedApplicationModel` returns untyped handles, so the AppHost cannot find a resource by name and call `WithParentRelationship`, `WithContainerRuntimeArgs` or `WithTags` on it. Resources an integration creates, such as the `<frontend>-installer` steps, cannot be reached at all, so they stay untagged. | `model.go` keeps its own index of the resources it adds (dashboard nesting, compose labels, the tunnel's waits). | [microsoft/aspire#22572](https://github.com/microsoft/aspire/issues/22572) |
| `dashboard-from-containers` | Not Go-specific. A container reaches a host process only through Aspire's container tunnel, and only to an endpoint of the model. The dashboard's OTLP endpoint gets there through `WithOtlpExporter`, but nothing gives a container the dashboard's frontend, where its telemetry API is. `host.docker.internal` only works on Docker Desktop and OrbStack; on Docker Engine on Linux it is the bridge address, where the dashboard does not listen (reported on ICTU/GBO-demo#724). | The collector uses `WithOtlpExporter`. dev-portal-backend uses `dashboard-api`: a host process (`tail -f /dev/null`) whose only job is an endpoint that Aspire proxies to the dashboard's port, so containers reach it through the tunnel. Remove it once a container can reference the dashboard. | Not filed |
| `launch-profile` | `aspire run --launch-profile` is refused for Go AppHosts ("not supported for Go AppHosts"), and with an `https` profile present the CLI picks it over the first one. | One `http` profile only; the demo profile comes from `Gbo__Profiles` instead of the `core`/`dvtp`/`full` launch profiles. Docs changed from `--launch-profile X` to `Gbo__Profiles=X aspire run`. | [microsoft/aspire#22571](https://github.com/microsoft/aspire/issues/22571) |

## Workarounds removed

- **`npm-installer`** (removed 2026-11-23, no Aspire change). The AppHost used
  to switch off the installer `WithNpm` creates and run `npm ci` itself with a
  Node.js 24+ it looked up, because the Go SDK cannot reach that installer
  (`FindResourceByName` returns an untyped handle, and the installer does not
  take the Vite app's environment) and the first `npm` on PATH may be older.
  That left five `NotStarted` installer resources in the dashboard. Now the
  frontends use `WithNpm(NpmInstall(true), NpmInstallCommand("ci"))`, and the
  AppHost stops with a hint when the first `node` on PATH is older than 24.
  Trade-off: Node.js 24+ must come first on the PATH `aspire run` starts with.
  The installers still cannot be tagged (typed-model).

## Gaps that do not affect this AppHost

- **Brownfield `aspire init` next to a `go.mod`** fails
  ([microsoft/aspire#21201](https://github.com/microsoft/aspire/issues/21201)).
  The repository root has no `go.mod` and the AppHost lives in `aspire/` with
  its own module, so this AppHost was written by hand and never needed init.
- **Certificate-trust and OTel Collector APIs being C#-first.** The plan
  expected these to matter. The AppHost uses neither: the FSC PKI is made by
  the `fsc-infra/pki` scripts and the collector is a plain container.
- **The custom-Dockerfile entrypoint bug and Kubernetes publish** concern
  `AddGoApp` and the Kubernetes publisher. The services are `AddDockerfile`
  containers and the AppHost only publishes to Docker Compose.

## Gaps without a workaround

- **Editor support.** CodeLens and gutter actions in VS Code do not read
  `apphost.go`
  ([microsoft/aspire#21201](https://github.com/microsoft/aspire/issues/21201)).
  `aspire run` and the dashboard are unaffected.
- **Agent skills.** The Aspire skills do not detect a Go AppHost yet
  ([microsoft/aspire#21201](https://github.com/microsoft/aspire/issues/21201)).
- **No API freeze.** The generated SDK has no baseline or compatibility
  check yet
  ([microsoft/aspire#22259](https://github.com/microsoft/aspire/issues/22259)),
  so names can change between Aspire releases; `aspire restore` then
  regenerates `.aspire/modules` and `apphost.go` may need edits.
- **Support policy** for Go AppHosts is still open
  ([microsoft/aspire#21203](https://github.com/microsoft/aspire/issues/21203)).

## Not checked at run time

The same as on the C# branch: the EUDI, ASI, credential-issuer and manager
parts need the development CAs and public URLs this machine does not have, so
they were checked through `aspire publish` only (identical compose output),
not with `aspire run`. `core` was started once; `full` was not.

## Upstream issues

Checked against microsoft/aspire `main` (commit `f9da20e`, 2026-11-23): all
five workaround gaps are still there, and no issue or pull request covers
them. Filed on 2026-11-23 as the issues linked in the workaround table, with
fixes proposed for the two bugs:
[microsoft/aspire#22573](https://github.com/microsoft/aspire/pull/22573) for
`nil-args` and
[microsoft/aspire#22574](https://github.com/microsoft/aspire/pull/22574) for
`parameter-arg`. When a release contains them, remove the two workarounds.
Where each gap sits in the Aspire source:

| Gap | Kind | Where |
| --- | --- | --- |
| `nil-args` | bug | the nil guard for required parameters in `src/Aspire.Hosting.CodeGeneration.Go/AtsGoCodeGenerator.cs` also skips nil slices |
| `parameter-arg` | bug | `CommandLineArgsEditor.Add` and `EnvironmentEditor.Set` store the parameter's builder; `ProcessValueAsync` in `src/Aspire.Hosting.Docker` does not unwrap it |
| `launch-profile` | feature | `GuestAppHostProject.SupportsLaunchProfiles` is `false` and `ReadProfileFromAspireConfig` prefers `https`, then the first profile (`src/Aspire.Cli/Projects`) |
| `typed-model` | feature | `FindResourceByName` (`src/Aspire.Hosting/Ats/ModelExports.cs`) returns `IResource`, which the Go SDK wraps without methods |
| `string-args` | by design | the `object[]` overload of `WithArgs` is marked `AspireExportIgnore`; `WithArgsCallback` is the intended route |

`parameter-arg` is wider than the workaround shows: a parameter set through
`WithEnvironmentCallback` publishes without an error, but with a .NET type
name as its value in `docker-compose.yaml`. This AppHost sets environment
variables with `WithEnvironment`, which is not affected.

## Moving from the C# AppHost

The Go AppHost has its own user-secrets store. Its generated passwords would
not match the Postgres volumes the C# AppHost created, so copy the existing
values over once, in `aspire/`:

```bash
for k in AppHost:DashboardApiKey AppHost:OtlpApiKey Parameters:fsc-postgres-password Parameters:ftv-adl-reader-password Parameters:ftv-postgres-password; do
  aspire secret set "$k" "$(cd ../../GBO-demo-aspire/aspire && aspire secret get "$k" --non-interactive | tail -1)"
done
```

Add any other `Parameters:*` the old store has (`aspire secret list` there).
