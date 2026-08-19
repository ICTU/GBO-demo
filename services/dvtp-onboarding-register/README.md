# DvTP onboarding register

Demo service for admitting private parties to one or more sources in the DvTP
chain. Adding a participant means the legal onboarding checks are considered
complete for this demo.

The register stores only:

- the participant's 20-character alphanumeric FSC Peer ID;
- its display name;
- whether the admission is active;
- the FSC Peer IDs of the source holders to which it is admitted.

Source holders, technical system participants and optional demo seeds are
deployment configuration, loaded from `ONBOARDING_CONFIG_PATH` (default:
`/config/onboarding.json`). The image contains `config/demo.json` for local
Compose; a deployment should mount its own file. Technical participants are
included in the internal OpenFTV feed but are neither persisted nor editable
through the UI.

Kubernetes examples are provided in
[`deploy/helm/gbo-app/examples/dvtp-onboarding-configmap.yaml`](../../deploy/helm/gbo-app/examples/dvtp-onboarding-configmap.yaml)
and
[`deploy/helm/gbo-app/examples/dvtp-onboarding-register-values.yaml`](../../deploy/helm/gbo-app/examples/dvtp-onboarding-register-values.yaml).
The ConfigMap is deployment-owned because Peer IDs identify the actual FSC
organizations in that environment; it must be deployed with the register and
the matching OpenFTV PIP mapping.

The server-rendered UI is available at `http://localhost:9415`. Compose binds
that port to host loopback only. Mutating forms require a per-process CSRF
token and a same-origin browser request. OpenFTV reads
`/internal/openftv/participants` over a dedicated Docker network using its
native PIP pull configuration; the endpoint is not published as a public
internet endpoint by this deployment. The register and PDP share a dedicated
network that no other backend service joins.

This is intentionally a mock, without user accounts or an onboarding
workflow: anyone with local access to the UI is an administrator. A production
deployment should put the administration UI behind its
normal operator authentication and keep the pull endpoint on a private network
or protect it with mTLS. OpenFTV pull configurations support `tlsCA`, `tlsCert`
and `tlsKey`, so that does not require changing the policy or response shape.

Participants are deactivated rather than deleted. OpenFTV's pull currently
adds or replaces returned entities but does not remove an entity merely because
it disappeared from a later response; retaining an explicit inactive record
therefore keeps revocation fail-closed.

## Structure

The admission rules and use cases live in `internal/onboarding`, together with
the repository port they consume. `internal/httpapi` is the driving adapter and
`internal/sqlite` is the persistence adapter. `main.go` only loads configuration,
wires those parts and manages the process lifecycle.
