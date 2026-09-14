# `gbo-app`

One generic chart, one application per release. A release is described
entirely by its values file; the chart itself holds no per-application
knowledge.

## Two release shapes

A release is either a workload that serves traffic or a task that runs to
completion. The two are mutually exclusive, and `job.enabled` picks between
them:

| `job.enabled` | Renders | For |
| --- | --- | --- |
| `false` (default) | Deployment, Service, optional HTTPRoute | long-running services |
| `true` | Job | work that runs once and exits |

Both shapes render from the same pod spec (`gbo-app.podSpec` in
`_helpers.tpl`), so images, environment, volumes and security contexts behave
identically in either. A Job release drops what only a server needs: the
container port, the Service and the HTTP probes. Setting `route.enabled` on a
Job release is a template error rather than a silently ignored value.

Anything that must happen *before this pod's own main container starts* is
neither of these: it is an entry in `initContainers`, which is passed through
as raw Kubernetes YAML.

## Running a task exactly once per release

The Job is named `<fullname>-<release revision>`. A Job spec is immutable, so
a release that reruns a task has to create a new object rather than patch the
old one; the revision suffix gives every `helm upgrade` its own Job and makes
`kubectl get jobs` read as a history of attempts. Helm removes the previous
revision's Job as part of the upgrade.

What the chart guarantees, and the reason schema migrations use this shape
rather than an init container:

- the task runs **once per release**, not once per pod start;
- **one pod per Job**: `parallelism` and `completions` are pinned to 1 and
  `replicaCount` is ignored, so a single release never races itself;
- a failure stays visible. `restartPolicy: Never` keeps failed pods for
  `kubectl logs`, `backoffLimit` bounds the retries, and
  `activeDeadlineSeconds` fails a wedged migration instead of hanging.

### Waiting for the Job

Plain `--wait` does **not** wait for Jobs in Helm 3; it only waits for
workloads to become ready. Install and upgrade a Job release with
`--wait --wait-for-jobs`, or a release that is still migrating — or has
already failed to — reports success, and whatever is ordered after it starts
against an unmigrated database. Flux's `HelmRelease` waits for Jobs by default
on install, upgrade and rollback; leave `disableWaitForJobs` unset.

### Overlapping attempts are the migration's problem, not the chart's

One pod per Job does not mean one pod per database. On upgrade Helm creates the
new revision's Job *before* it deletes the previous one, and deletes it with
background propagation, so the old pod can still be running when the new one
starts. The typical path is a retry after a Helm timeout. The chart has no way
to see or stop that pod without Kubernetes API access it deliberately lacks.

Mutual exclusion therefore lives where the shared state lives: in the
database. Every migration release must tolerate a concurrent twin, and each of
the three does:

| Release | How an overlapping attempt is excluded |
| --- | --- |
| `source-registry-bootstrap` | a session-level `pg_advisory_lock` in each psql session of `scripts/bootstrap-source-registry.sh`; the second run waits |
| `source-registry-migrations` | `Store.Migrate` holds `pg_advisory_lock` on a dedicated connection for the whole run; the second run waits. The reconciler's `--migrate` takes the same lock |
| `eudi-migrations` | no lock upstream, but sea-orm applies the whole run in one PostgreSQL transaction and `seaql_migrations.version` is a primary key. Two attempts cannot both commit a migration: the loser rolls back entirely, fails, and its retry finds nothing left to do |

A new migration release needs the same property before it gets a values file
here: a database lock, or all-or-nothing transactional migrations with a
uniquely keyed history table.

`ttlSecondsAfterFinished` is unset by default on purpose: the finished Job is
what you read when a migration went wrong. Set it where the record is not
worth keeping.

## Where each one-shot EUDI workload lives

`docker-compose.yml` has four workloads that run once and exit. Each has a
place here:

| Compose service | Chart shape | Example values |
| --- | --- | --- |
| `source-registry-bootstrap` | Job | [`source-registry-bootstrap-values.yaml`](examples/source-registry-bootstrap-values.yaml) |
| `source-registry-migrations` | Job | [`source-registry-migrations-values.yaml`](examples/source-registry-migrations-values.yaml) |
| `eudi-migrations` | Job | [`eudi-migrations-values.yaml`](examples/eudi-migrations-values.yaml) |
| `eudi-issuance-materialize` | init container | [`eudi-issuance-server-values.yaml`](examples/eudi-issuance-server-values.yaml) |

`eudi-issuance-materialize` is the one that is not a Job. It writes
`issuance_server.toml` into an `emptyDir` that the issuance server then reads,
so it must run again for every pod, in that pod, before the server starts —
exactly what an init container guarantees and what a separate Job cannot.

The three database tasks are the opposite: they change state that outlives any
pod, they must not run twice at once, and their ordering is across releases
rather than within a pod.

Compose's `depends_on: service_completed_successfully` has no Kubernetes
equivalent in this chart. Order the releases instead — bootstrap, then
migrations, then the workloads — with Flux `dependsOn` or by applying them in
sequence.

## Workload kinds, and why there is no StatefulSet

`postgres-eudi` deploys as a Deployment with one replica, `Recreate` and a
ReadWriteOnce PVC — see
[`postgres-eudi-values.yaml`](examples/postgres-eudi-values.yaml), which
records the reasoning. In short: `Recreate` already guarantees the property
that matters (the old pod releases the volume before the new one attaches, so
two postgres processes never open one data directory), while a StatefulSet's
per-replica identity and per-replica claims buy nothing for a single replica
addressed only through its Service. Revisit it if the demo grows replicas or
replication.

## Examples

Every file in `examples/` is a values file for one release, except
`dvtp-onboarding-configmap.yaml`, which is a plain manifest the register
release mounts. CI renders each of them on every push, so an example that
stops templating fails the build.
