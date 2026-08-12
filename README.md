# GBO — Gemeenschappelijke Bronontsluiting (referentie-demo)

A live, runnable reference architecture for **GBO** (Gemeenschappelijke Bronontsluiting). It shows how a consumer (data requester) can obtain data from a source-holder over a trusted transport channel, where every request is authorized against machine-readable policy, and where the source retains control over identifier resolution.

Two access flows sit side-by-side on the same authorization pipeline:

- **Consent flow** — a citizen grants a consumer permission to query a specific scope of source data. The consumer uses a consent-id to trigger the query; the source resolves it against the citizen's real identifier (BSN) inside its own trust boundary.
- **Wallet flow** — a citizen holds an EUDI-wallet credential and discloses it to a consumer, who then requests source data using the disclosed identifier. Same policy engine, same transport, different front door.

Both flows share one authorization pipeline: FSC-Inway (transport) → OpenFTV PDP (OPA/Rego policy engine + GraphQL request-mapper) → source-side sidecar (identifier substitution) → source.

## Prerequisites

- **Docker** with Compose plugin (Docker Desktop 4.x or Docker Engine + `docker compose`).
- **~8 GB RAM** allocated to Docker (Preferences → Resources), **~10 GB disk** for images.

Every mode also needs two Postgres passwords set via env-files (compose fails loud if unset — any string works for the local demo network):

- **`EUDI_POSTGRES_PASSWORD`** in `.env` — password for `postgres-eudi` (issuance-server + migrations).
- **`FSC_POSTGRES_PASSWORD`** in `fsc-infra/.env` — shared password for all FSC-infra database users (three orgs × controller/manager/txlog + directory).

That covers the default (`make demo`, DvTP-only). For the wallet flow (`make demo-eudi` / `make demo-full`) you also need:

- **nl-wallet sources** — pinned as a git submodule at `vendor/nl-wallet` (**v0.5.0**). Server and app move in lockstep: v0.5.0 made the `x509_san_dns:` `client_id` prefix mandatory, so a v0.4.1 wallet app cannot complete a session against a v0.5.0 server, or the other way round. Init once with `git submodule update --init vendor/nl-wallet`. Used to build the issuance-server binary from source. Override with `NLWALLET_PATH` in `.env` if you need another checkout.
- **Two public HTTPS URLs** — `EUDI_PUBLIC_URL` (wallet reaches issuance-server) and `EUDI_BRI_URL` (issuance-server reaches eudi-adapter). See [EUDI public reachability](#eudi-public-reachability) for the three supported options (own domain / bundled Cloudflare tunnel / ad-hoc tunnel).

  Since v0.5.0 the `EUDI_PUBLIC_URL` **host** is part of the crypto, not just routing: the issuance-server derives its OpenID4VP `client_id` from it (`x509_san_dns:<host>`) and validates on startup that every reader certificate carries that host as a DNS SAN. Point `EUDI_PUBLIC_URL` at a different hostname and provision new development reader certificates before reconciling the sources again — see [Reader certificates and `EUDI_PUBLIC_URL`](#reader-certificates-and-eudi_public_url).
- **Activated sources with certificate references** — `make onboard-demo-sources` explicitly provisions local-development certificates and activates BD and BRP. Each source publishes its concrete wallet products as `offers`; `make eudi-config` generates the issuance-server TOML, Type Metadata files, and frontend QR catalog from all active records. There is no source/use-case catalog in the adapter or issuance server. Production uses pre-managed certificates; [issue #225](https://github.com/ICTU/GBO-demo/issues/225) tracks the choice between shared and per-source certificate sets.

Copy the templates and fill them in:

```bash
cp .env.example .env
cp fsc-infra/.env.example fsc-infra/.env
# then edit both
```

## Quick Start

```bash
cd 05-demo
make demo             # DvTP (consent) flow only — no wallet, no public URLs needed
```

### Other modes

```bash
make demo-minimal     # Base only (~30s, ~13 services)
                      # Curl directly at the OpenFTV PDP /authzen/v1/evaluation for policy tests.

make demo-eudi        # Wallet flow only (~5-10 min first boot; PKI + FSC-infra + contract seed)
                      # Requires the vendor/nl-wallet submodule + two public HTTPS URLs
                      # — see "EUDI public reachability" below.

make demo-full        # Both flows on

make demo-down        # Bring everything down
```

Three front-ends run in parallel (in default/full mode):

- **Consumer mock** (`http://localhost:9001`) — a stand-in for a data-consuming party (e.g. a mortgage lender). Talks to `dienstverlener-backend`.
- **Consent portal** (`http://localhost:9002`) — a citizen-facing UI to grant and revoke consent. Talks to `consent-portal-backend`.
- **Developer portal** (`http://localhost:9003`) — architect inspection UI: live trace view + policy inspection + FSC txlog per hop.

The developer portal also runs in `demo-minimal` and `demo-eudi` — flow tabs stay empty until the matching backend services are up.

## Demo Walkthrough (Consent Flow)

1. Open the **consent portal** (`:9002`) and log in as a citizen (mock DigiD, BSN from `graphql-server/mockdata/citizens.json`).
2. Grant consent for a scope (e.g. `bd:ib:2025`) to a consumer.
3. Open the **consumer mock** (`:9001`), enter the consent-id, click **"Run query"** — the consumer queries income data via HV-Outway → BD-Inway → AuthZen call to PDP → OpenFTV → source sidecar (PI→BSN) → GraphQL.
4. Open the **developer portal** (`:9003`) → Use tab → click "Watch" → the live arch strip lights up hop by hop.
5. Revoke consent from the portal and repeat the query — the OpenFTV PDP denies with `CONSENT_WITHDRAWN`.

## "Break things" Guide

### Edit policies (Rego hot-reload)

The OpenFTV PDP watches the `policies/` directory. Edit any Rego file and save — the PDP reloads automatically within a few seconds.

```bash
# Example: force the PDP to deny everything
echo 'package authz
default allow := false
reason := "POLICY_OVERRIDE"' > policies/authz.rego

# Run a query — the deny surfaces in the consumer UI and the developer portal.

# Restore the original policy
git checkout policies/authz.rego
```

Not always, though: the watcher has been seen to miss `lib.rego` while picking
up every other file in the same tree (issue #151). If a policy change has no
effect, restart the engine before concluding the rule is wrong:

```bash
docker compose restart openftv-pdp
```

### Policy distribution via the OpenFTV Manager (`make demo-manager`)

The loop above edits files the PDP reads directly. That is convenient while
writing Rego, but it is not how a federation distributes policy: every
source-holder would need to rebuild or remount its PDP to change a rule.

`make demo-manager` starts the other half of OpenFTV — the **Manager**, which
is the PAP and PIP in one service. Git remains the canonical policy source.
An internal deployment identity seeds those policies into the Manager's
Postgres, after which the Manager bundles them and serves each bundle to the
PDPs that ask for it. The PDP pulls its bundle at boot
(`PDP_BUNDLE_MANAGER`), and that deployed bundle is its runtime policy set.

```bash
KEYCLOAK_ADMIN_PASSWORD='<generate-a-secret>' \
FTV_MANAGER_AUDITOR_PASSWORD='FDSSecret' \
FTV_MANAGER_DEPLOY_PASSWORD='<generate-another-secret>' \
FTV_POSTGRES_PASSWORD='<generate-a-database-secret>' \
make demo-manager                 # base stack + Manager, policies seeded
curl -s localhost:9281/v1/bundle/gbo-pdp | gunzip | jq '.version, (.policies|length)'
```

Editing `policies/` no longer reloads by itself. Deploy the changed Git state
deliberately, using the same secret-backed identity:

```bash
FTV_MANAGER_DEPLOY_PASSWORD='<same-secret>' make manager-seed
```

### The management interface

`make demo-manager` also starts OpenFTV's own UI with a Keycloak realm behind
it. Public demo visitors use the simulation credentials `fds` / `FDSSecret`
and can inspect policies, deployments and the Logboek, but cannot change or
publish anything. `FTV_MANAGER_AUDITOR_PASSWORD` supplies that password to
Keycloak, so a deployment can manage it as a CI/CD secret. The API
enforces that restriction with Cedar policies; it is not just a disabled UI
button. Only the internal `deployment` identity, whose password comes from
`FTV_MANAGER_DEPLOY_PASSWORD`, can write and publish. There is deliberately
no public admin or author account. The UI reads capabilities from a top-level
`roles` claim in the access token, which is why it is Keycloak; see
`services/openftv-manager-ui/README.md`.

**It must be served over HTTPS unless you open it on `localhost`.** The OIDC
login uses PKCE, PKCE needs `Crypto.subtle`, and browsers only expose that in
a secure context — over `http://<lan-ip>:9283` the page fails with
*"Crypto.subtle is available only in secure contexts"*. Put the UI, the
Manager API and Keycloak behind a TLS reverse proxy and point the stack at
them:

```bash
GBO_FTV_MANAGER_URL=https://ftv-api.example.org \
GBO_FTV_OIDC_AUTHORITY=https://ftv-auth.example.org/realms/gbo \
  make demo-manager
```

Keycloak needs `KC_PROXY_HEADERS=xforwarded` (set in compose) and the proxy
must send `X-Forwarded-Proto: https`, or its login form posts over HTTP from
an HTTPS page and the browser blocks it as *"Form is not secure"*.

The public auditor can also inspect the **Logboek** (decision log). That is
appropriate only because this simulation uses synthetic data; do not copy
that access model to a tenant containing personal data, tokens or secrets.
The Logboek needs the ADL on Postgres at both ends: the
Manager only registers those routes for a Postgres-backed decision log —
any other value and the endpoints simply do not exist, so the page 404s —
and the PDP has to write there. `make demo-manager` sets both. The ADL gets
its own database (`ftv_adl`): it carries its own migration set while the
Manager's schema is at version 10, and both record progress in a
`schema_migrations` table, so sharing one database makes the PDP's
migration fail with *"no migration found for version 10"* and the PDP then
refuses to start.

This is OpenFTV's own ADL, and is independent of the embedded OPA console
decision log that the developer portal reads from Loki.

Three things worth knowing before relying on it:

- **Managed Rego policies have no file store.** They arrive through the API.
  `scripts/seed-openftv-manager.py` obtains a short-lived token for the
  internal deployment identity and pushes `policies/` in. `/authz` in the
  image is separate: it contains only the Cedar policies that protect the
  Manager's own API. The Manager fails closed if those cannot be loaded.
- **Policies cannot be deleted.** `DELETE /v1/policy/:id` fails with a
  foreign-key violation on `policy_audit` (upstream bug). The seed script
  therefore *retires* a policy that has left git by stripping the bundle's
  tag, which drops it from the bundle. This matters: the store is
  cumulative, and two policies declaring the same `package` make the PDP
  fail to compile the whole set, so every request 500s.
- **The Manager serves bundles; it does not push them here.** It can POST a
  bundle to a PDP, but the PDP authorizes its own management endpoints with
  the same policy set it evaluates requests against — our `package authz`
  entrypoint — so a push is denied with 403. Allowing it would mean opening
  the PDP's management API from the GBO decision policy, which is not a
  trade worth making for convenience. OpenFTV only bypasses this when the
  policy store is empty, i.e. never in practice. So `targets` is empty and
  the PDP pulls at boot, which is why `make demo-manager` restarts it after
  seeding.
- **Masking still does not work.** Adopting the Manager does not change it:
  bundle-delivered policies reach the engine through the same `UpsertPolicy`
  path as file-delivered ones, never through OPA's bundle plugin, so
  `data.system.log.mask` never resolves either way.

The default stack is unchanged — without `GBO_BUNDLE_MANAGER` the PDP skips
bundle retrieval entirely and keeps loading `policies/` from disk.

The Manager is deliberately only in the `manager` profile, not `full`: a
management plane should never appear accidentally without its required
secrets and seed step. After upgrading an existing prototype Manager volume,
recreate that volume once so the Cedar bootstrap policies can be seeded into
an otherwise empty store.

### Revoke consent

Click "Revoke consent" in the consent portal (`:9002`), repeat the query. The PDP reads the consent register, sees status=REVOKED → DENY.

### View decision logs

```bash
docker compose logs -f openftv-pdp
```

## Service ports

| Service | Port | Description | Real/Mock |
|---------|------|-------------|-----------|
| Consumer mock | 9001 | Consumer UI (React/Vite) | Demo frontend |
| Consent portal | 9002 | Citizen UI (React/Vite) | Demo frontend |
| Developer portal | 9003 | Architect inspection (React/Vite) | Demo frontend |
| dev-portal-backend | 9407 | Trace hub + explain endpoint | Real (Go) |
| GraphQL Server | 9400 | Sample source with income data | Real (Go) |
| bron-sidecar | 9411 | Source-side gateway; PI→BSN via BSNk (subject_id_type-driven) | Real (Go) |
| additional-claims-service | 9412 | Provider policy that enriches OpenFSC access tokens | Demo configuration (Go) |
| Consent Register | 9402 | Consent store (PIP) | Mock (Go, in-memory) |
| BSNk Mock | 9403 | Pseudonymization service | Mock (Go, deterministic) |
| HV-Manager UI | 8096 | Consumer-org FSC-Controller (mortgage-lender demo org) | Real (OpenFSC v2.4.0) |
| EDI-Manager UI | 8094 | Consumer-org FSC-Controller (EUDI issuer) | Real (OpenFSC v2.4.0) |
| BD-Manager UI | 8092 | Provider-org FSC-Controller (source-holder demo org) | Real (OpenFSC v2.4.0) |
| OpenFTV PDP | 9181 (API, HTTPS) / 9180 (health) | Policy Decision Point (OPA/Rego engine + GraphQL context-mapper) | Real |
| Jaeger | 9686 | Distributed tracing UI | Real |
| OTel Collector | 9317 | Trace collection | Real |

## What is real vs. demo scaffolding

| Component | Status | Notes |
|-----------|--------|-------|
| OpenFTV PDP / Rego policies | **Real** | OpenFTV PDP (embedded OPA) with real Rego evaluation |
| OpenTelemetry + Jaeger | **Real** | Production-grade distributed tracing |
| GraphQL Server | **Real** | Real Go GraphQL implementation |
| FSC (Manager/Inway/Outway/Controller/txlog) | **Real** | OpenFSC v2.4.0 upstream containers, three orgs (consumer, EUDI-issuer, provider) each with their own PostgreSQL + certs |
| bron-sidecar | **Real** | Source-side gateway; PI→BSN driven by the signed `subject_id_type` additional claim |
| additional-claims-service | **Demo** | GitOps-style provider policy; production should resolve claims from the authoritative onboarding or authorization source |
| EUDI PID disclosure | **Demo** | The subject the EUDI rules key on (`pip.pid.pi`, derived by the request-mapper from the disclosed BSN via BSNk) originates in the same request that carries the query variable selecting the record — it is not independently verified, so the disclosure does not prove the subject. Production needs the wallet's verified PID assertion bound to `variables.bsn` before the PDP evaluates. Applies to both EUDI flows (BD and BRP) |
| Consent Register | **Mock** | In-memory; production would be a persistent store |
| BSNk Mock | **Mock** | Deterministic SHA-256; real BSNk uses ElGamal on elliptic curves |

## Architecture

The five-factor authorization model demonstrated:

| # | Factor | Implementation in demo |
|---|--------|------------------------|
| ① | Org identity (mTLS) | FSC-Manager validates peer-certs; FSC-Inway includes peer_cert_chain in the AuthZen context |
| ② | Org permission (JWT) | Provider FSC-Manager validates the grant and signs `add.{flow, subject_id_type}` returned by its Additional Claims API |
| ③ | Access basis (consent) | The request-mapper fetches the ACTIVE consent for (PI, scope) from the consent-register per request, so a revoke takes effect immediately |
| ④ | Data scope (GraphQL) | The OpenFTV PDP checks every requested field against the applicable rule (DVT0001/EUD0001/EUD0002) |
| ⑤ | Request validity | The OpenFTV PDP validates consent + `context.resource.pi` binding + expiry |

## Makefile targets

```bash
make up      # Build and start all services
make down    # Stop all services
make logs    # Tail all service logs
make clean   # Stop, remove volumes and images
make certs   # Generate self-signed TLS certificates
```

### FSC-infra targets

Real OpenFSC transport with self-hosted root-CA + directory-peer. Fully standalone from the main stack.

```bash
make fsc-ca    # Generate root-CA + intermediate-CA in fsc-infra/pki/ca/ (idempotent)
make fsc-up    # Start cfssl + certportal (implies make fsc-ca)
make fsc-test  # Verify: test-CSR → certportal → chain-check
make fsc-down  # Stop the fsc-infra containers
make fsc-clean # Wipe everything: containers, images, CA material
```

## Real FSC end-to-end

Three FSC orgs run alongside the main stack:

- **Consumer-org (mortgage lender)** — consent-flow consumer; provider claims resolve to `flow=dvtp:query`, `subject_id_type=pseudonym`
- **EDI-Issuer** — wallet-flow consumer; provider claims resolve to `flow=eudi:attestation`, `subject_id_type=direct`
- **Provider (source-holder)** — provides the `bri` service; endpoint routes through the bron-sidecar

`make demo` orchestrates the full sequence automatically:
- PKI generation (root-CA + per-org certs)
- FSC-infra start (three orgs + directory-peer)
- Contract seed (bri-service + publication + two connection contracts + grant-links)
- Main stack with dienstverlener-backend, eudi-adapter, openftv-pdp, bron-sidecar

Step-by-step targets are available for debugging:

1. **`make fsc-all-up`** — FSC-infra + orgs. The directory-manager runs with `--auto-sign-grants=servicePublication`; the provider-manager runs with `--auto-sign-grants=serviceConnection`. Contracts reach `CONTRACT_STATE_VALID` without manual review.

2. **`make fsc-seed-bri`** + **`bash fsc-infra/scripts/seed-bri-connection-hv.sh`** — services + contracts + grant-links. Registers the `bri` service in the provider-Controller (endpoint = bron-sidecar), posts publication + two connection contracts, and upserts the grant-links per consumer. The provider Manager obtains flow and identifier semantics from `additional-claims-service` when issuing an access token. Idempotent.

   Grant-link upsert goes via direct SQL — v2.4.0 has no REST endpoint for grant-link CRUD.

3. **Generate OpenFTV PDP TLS cert + restart**:
   ```bash
   bash fsc-infra/pki/generate-pdp-cert.sh
   docker compose up -d --force-recreate dienstverlener-backend eudi-adapter openftv-pdp graphql-server bron-sidecar
   docker compose -f fsc-infra/docker-compose.yml up -d --force-recreate bd-inway
   ```

   `generate-pdp-cert.sh` produces a self-signed cert (SAN=`openftv-pdp`) — FSC-Inway's AuthZen plugin requires HTTPS+CA. The same `.pem` is mounted by the provider-inway as `AUTHZEN_ROOT_CA`.

**Reset**:

```bash
make fsc-down
docker volume rm fsc-infra_postgres-data    # wipe all contracts/publications
```

Without a volume wipe, contracts and grant-links survive a restart.

## EUDI public reachability

Voor het lokaal registreren, valideren en provisionen van door de bron
geleverde attestatiedefinities, zie
[Lokale onboarding van bronmetadata](docs/source-onboarding.md).

The EUDI flow needs two publicly-reachable HTTPS URLs:

- `EUDI_PUBLIC_URL` — the wallet on a phone opens this to talk to the `issuance-server`.
- `EUDI_BRI_URL` — the `issuance-server` fetches attestations from the `eudi-adapter` at this URL.

Both values are read from `.env`. Pick whichever way to expose the two services fits your setup:

`make demo-eudi` runs source reconciliation and `make eudi-config` before the
stack starts. The generated products come from `offers` in the active source
metadata. Because the pinned nl-wallet issuance-server reads product settings
only at startup, a later activation change requires config regeneration and a
controlled restart/rollout of the issuance-server and frontends.

The wallet `client_id` is derived as `x509_san_dns:<host>` from
`EUDI_PUBLIC_URL`, matching the DNS SAN of the provisioned reader certificate.
There is no separate client-ID setting.

The developer-portal container writes `EUDI_PUBLIC_URL` to
`/runtime-config.js` when it starts. This allows Kubernetes and other runtime
environments to configure wallet QR links without rebuilding the frontend.
For backwards compatibility, the container also accepts
`VITE_EUDI_PUBLIC_URL`.

**(a) Own domain / reverse proxy** — point two HTTPS hostnames at the compose ports and set the URLs. Nothing else to install.

**(b) Cloudflare named tunnel (bundled)** — one Cloudflare tunnel with two Public Hostnames configured in the dashboard, plus the connector token in `.env`:

```bash
# In .env
CLOUDFLARE_TUNNEL_TOKEN=eyJ...
EUDI_PUBLIC_URL=https://eudi-is.your-cf-hostname.tld/
EUDI_BRI_URL=https://eudi-bri.your-cf-hostname.tld/

# Start the tunnel alongside the EUDI stack
docker compose -f docker-compose.yml -f docker-compose.cloudflare-tunnel.yml --profile eudi up -d
```

**(c) Ad-hoc tunnel** (ngrok, `cloudflared --url`, `tailscale funnel`, …) — start it yourself, paste the two URLs into `.env`, then bring up the stack without the tunnel file.

### Reader certificates and `EUDI_PUBLIC_URL`

Whichever option you pick, the hostname in `EUDI_PUBLIC_URL` is not free. nl-wallet v0.5.0 derives the OpenID4VP `client_id` from it — `x509_san_dns:<host>` — and `WalletInitiatedUseCase::try_new` refuses to build a disclosure use case unless the reader certificate for that use case has the same host among its DNS SANs. A mismatch is a startup failure, not a runtime one:

```
public url host <host> not in certificate DNS SANs: <sans>
```

Every activated source therefore needs a reader certificate for the host you are actually serving on. For the local proof, rerun the explicit development provisioning step for both sources and reconcile them:

```bash
make provision-development-certificates SOURCE_OIN=99999999900000000200 SOURCE_NAME="Belastingdienst" SOURCE_LOGO=assets/issuer-logos/belastingdienst.svg
make provision-development-certificates SOURCE_OIN=99999999900000000400 SOURCE_NAME="RvIG" SOURCE_LOGO=assets/issuer-logos/rvig.svg
make reconcile-fsc-sources
make eudi-config
```

The command reuses the existing development private keys and CA while replacing stale leaf-certificate content. `SOURCE_LOGO` embeds an SVG, PNG, or JPEG as nl-wallet organization metadata in the issuer and reader authorization extensions. It is explicit certificate-provisioning input, not source-published metadata. The included SVGs are recognizable demo assets rather than official brand files. `requestOriginBaseUrl` follows `EUDI_PUBLIC_URL`; the frontends derive the same client ID and need no separate setting.

The local command stores its issuer and reader CAs under `.local/secrets/development-ca`. A real wallet only accepts leaves under a configured trust anchor, so use pre-approved development CA material in that directory when testing with the preproduction wallet. **Never create a new CA merely to solve a hostname mismatch**: an unknown root is rejected regardless of the leaf certificate.

### The other certificates

Issuer and status certificates are not tied to `EUDI_PUBLIC_URL`. Within each activation, the issuance server requires the attestation and status-list certificates to represent the same subject. The activation record references the corresponding issuer, reader, and status material; the config generator copies those references into the generated nl-wallet settings.

The current proof retains separate references for BD and BRP. It deliberately does not decide whether production should share one GBO certificate set, keep source-specific sets, or use a hybrid model. That decision, including issuer identity, key custody, trust registration, rotation, and revocation, is tracked in [issue #225](https://github.com/ICTU/GBO-demo/issues/225).

## Testing

```bash
# Go happy-path integration tests (per service)
for svc in additional-claims-service bron-sidecar bsnk-mock consent-portal-backend consent-register \
           dev-portal-backend dienstverlener-backend eudi-adapter \
           graphql-server sector-pip; do
  (cd services/$svc && go test -timeout 60s ./...)
done

# Rego policy unit tests (via the OPA CLI image)
docker run --rm -v $(pwd)/policies:/w -w /w openpolicyagent/opa:1.9.0-static test /w -v
```

CI (`.github/workflows/ci.yml`) runs both on every PR.

## Release

Pushing a SemVer tag with a `v` prefix starts `.github/workflows/release.yml`.
The workflow builds 13 application images for `linux/amd64` and `linux/arm64`
and builds `eudi-issuance-server` for `linux/amd64`. It publishes them to GHCR
with a tag without the `v` prefix plus `latest`, and then publishes the generic
`gbo-app` Helm chart with the same normalized version.
The Compose development flow is unchanged.

The `eudi-issuance-server` build uses the pinned `vendor/nl-wallet` submodule.
Clone or update this repository with submodules enabled before building it
locally:

```bash
git submodule update --init --recursive
```

Create a release after the release changes are on `main`:

```bash
git tag v0.1.0
git push origin v0.1.0
```

The resulting image and chart versions are `0.1.0`:

```bash
docker pull ghcr.io/ictu/gbo-demo/bsnk-mock:0.1.0
helm pull oci://ghcr.io/ictu/gbo-demo-charts/gbo-app --version 0.1.0
```

GHCR packages are private by default. After the first publication, set the 14
image packages and `gbo-demo-charts/gbo-app` to **Public** in the organization
package settings before testing anonymous pulls from tenant clusters.

For a local chart smoke test:

```bash
helm lint deploy/helm/gbo-app
helm template bsnk-mock deploy/helm/gbo-app \
  --set image.repository=ghcr.io/ictu/gbo-demo/bsnk-mock \
  --set image.tag=0.1.0 \
  --set containerPort=4003 \
  --set healthPath=/health
```

The chart also supports overriding the container entrypoint, passing arguments,
mounting native Kubernetes volumes, and selecting the health probe scheme. The
example values files cover the OpenFTV PDP (policies baked into the image) and a TLS-enabled PDP service:

```bash
helm template openftv-pdp deploy/helm/gbo-app \
  --values deploy/helm/gbo-app/examples/openftv-pdp-values.yaml
```

## Troubleshooting

**Services not starting?**
```bash
docker compose logs <service-name>
```

**PDP returning unexpected results?**
```bash
# Check decision logs
docker compose logs openftv-pdp | grep "Decision Log"

# Test the OpenFTV PDP directly (AuthZEN evaluation; HTTPS, self-signed)
curl -k -X POST https://localhost:9181/authzen/v1/evaluation \
  -H 'Content-Type: application/json' \
  -d '{"subject":{"type":"org","id":"test"},"action":{"name":"dvtp:query"},"resource":{"type":"graphql","id":"query"},"context":{...}}'
```

**Frontend not loading?**
- Check the three frontends (`dienstverlener-mock` :9001, `toestemmingsportaal-frontend` :9002, `developer-portal` :9003) and their backends.
- `docker compose logs <service>` for the container in question.

## Adding new access flows

The architecture is designed for incremental extension. Every new flow shares the same FSC-Inway → OpenFTV PDP (AuthZen + GraphQL request-mapper) → bron-sidecar → GraphQL chain — only the policy rules, contract properties, and entry points differ. Flow-specific context is provider-owned: OpenFSC asks `additional-claims-service` during token issuance and signs the returned values into the access token's `add` claim.

The checked-in mapping is deliberately small demo policy, not a second production contract register. A production deployment should resolve these claims from the same authoritative onboarding or authorization source that governs the relationship.

- **Legal-basis (gov-to-gov)**: add `policies/legal-basis/*.rego`, add a new FSC consumer org and configure provider claims with `flow=g2g:legal-basis`; the PDP dispatches on the signed token claim.
- **Wallet flow (already implemented)**: see `eudi-adapter`.
- **AS4 / SDG-OOTS**: add an AS4 bridge mock + Domibus mock.

## Repository owner

Owner: **Jeroen de Kok** — <jeroen.dekok@ictu.nl>

Please open an issue for questions or feature requests. For security issues see [SECURITY.md](SECURITY.md).
