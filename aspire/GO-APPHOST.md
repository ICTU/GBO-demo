# The Go AppHost: status

`apphost.go` replaces the C# file-based `apphost.cs` with a Go AppHost on the
Aspire 17.0 Go preview. This file records what works, what the preview does not
do yet, the workarounds in the code, and what cannot be worked around. Each
workaround is marked `TODO(go-apphost/<gap>)` in the code; remove it when the
upstream fix ships.

Checked with Aspire CLI 17.0.0, Go 1.27.4 on macOS (arm64), 2026-11-20.

## What works

- **The whole app model.** Every resource, profile, wait, mount, endpoint,
  parameter and setting of the C# AppHost is in `apphost.go`. `aspire publish`
  writes the same `docker-compose.yaml` and the same `.env` keys as the C#
  AppHost, apart from the generated passwords and the repository path, for:
  - the default profile (dvtp),
  - `core` (40 services),
  - every profile at once (`full,asi,manager,integrator,credential-issuer`,
    with the tunnel and both wallet verifiers: 170 services).
- **`aspire run` of the default (dvtp) demo**, on top of the volumes the C#
  AppHost made: all 122 resources come up (73 running and healthy, 44 run-once
  steps exit 0), in about 45 s to a healthy consumer backend. The DvTP flow
  works end to end in the browser: consumer mock, DigiD mock login, consent in
  MijnOverheid, back to the consumer with both income years fetched over FSC
  through the PDP. A restart on the same volumes comes up the same way.
- **Telemetry.** The collector forwards with the OTLP key the AppHost writes
  into the configuration; the dashboard telemetry API returns traces and the
  PDP's decision log (what the developer portal reads), and keeps them across
  restarts (Resume).
- **Settings** after `--` (`aspire run -- --Gbo:PublicHost=...`) reach the
  AppHost, as with C#.
- **No .NET SDK.** The Aspire CLI runs a prebuilt AppHost server; the only
  toolchain the AppHost needs is Go.
- **The launch profile** lives in `aspire.config.json` instead of
  `apphost.run.json`, with the same environment (Resume persistence,
  dashboard API key auth). Which demo profile runs is `Gbo__Profiles`
  (`Gbo__Profiles=core aspire run`), checked for `core` at run time.
- **Configuration** (`Gbo:*` settings, `Gbo__Profiles`, user secrets,
  `aspire secret set`) reads through `GetConfiguration()`, and the AppHost can
  write the dashboard keys into it, as C# did.
- **CI**: the OpenFTV version check and the CI planner read `apphost.go`.

## Gaps in the preview, with a workaround

| Gap (`TODO` tag) | What happens | Workaround | Upstream |
| --- | --- | --- | --- |
| `nil-args` | `AddExecutable(name, cmd, dir, nil)` leaves `args` out of the request; the server rejects it with "Missing required argument 'args'". | Pass an empty slice (`model.executable`). | Not filed yet |
| `parameter-arg` | A `ParameterResource` handle added through `WithArgsCallback` runs, but breaks `aspire publish` with "Command line args must be strings". C# passes `parameter.Resource`, which the Go SDK has no counterpart for. | Wrap it: `RefExpr("%v", p)` publishes as `${NAME}` (`model.argValue`). | Not filed yet |
| `string-args` | `WithArgs` takes `[]string`; C# takes `object[]` with parameters and expressions mixed in. | Mixed arguments go through `WithArgsCallback` (`model.containerArgs`, `model.executableArgs`). | Not filed yet (by design of the generated API, maybe) |
| `typed-model` | No typed `builder.Resources`; `DistributedApplicationModel` returns untyped handles, so the AppHost cannot find a resource by name and call `WithParentRelationship`, `WithContainerRuntimeArgs` or `WithTags` on it. Resources an integration creates, such as the `<frontend>-installer` steps, cannot be reached at all, so they stay untagged. | `model.go` keeps its own index of the resources it adds (dashboard nesting, compose labels, the tunnel's waits). | Not filed yet |
| `launch-profile` | `aspire run --launch-profile` is refused for Go AppHosts ("not supported for Go AppHosts"), and with an `https` profile present the CLI picks it over the first one. | One `http` profile only; the demo profile comes from `Gbo__Profiles` instead of the `core`/`dvtp`/`full` launch profiles. Docs changed from `--launch-profile X` to `Gbo__Profiles=X aspire run`. | Not filed yet |

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

None of the six workaround gaps had an upstream issue on 2026-11-20 (searched
microsoft/aspire). Each one is small and reproducible; filing them makes the
TODOs traceable. Not filed: that posts on GitHub and is your call.

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
