# GBO — Gemeenschappelijke Bronontsluiting (referentie-demo)

A live, runnable reference architecture for **GBO** (Gemeenschappelijke Bronontsluiting). It shows how a consumer (data requester) can obtain data from a source-holder over a trusted transport channel, where every request is authorized against machine-readable policy, and where the source retains control over identifier resolution.

Two access flows sit side-by-side on the same authorization pipeline:

- **Consent flow** — a citizen grants a consumer permission to query a specific scope of source data. The consumer uses a consent-id to trigger the query; the source resolves it against the citizen's real identifier (BSN) inside its own trust boundary.
- **Wallet flow** — a citizen holds an EUDI-wallet credential and discloses it to a consumer, who then requests source data using the disclosed identifier. Same policy engine, same transport, different front door.

Both flows share one authorization pipeline: FSC-Inway (transport) → PDP (context handler) → OPA (policy engine) → source-side sidecar (identifier substitution) → source.

## Prerequisites

- **Docker** with Compose plugin (Docker Desktop 4.x or Docker Engine + `docker compose`).
- **~8 GB RAM** allocated to Docker (Preferences → Resources), **~10 GB disk** for images.

Every mode also needs two Postgres passwords set via env-files (compose fails loud if unset — any string works for the local demo network):

- **`EUDI_POSTGRES_PASSWORD`** in `.env` — password for `postgres-eudi` (issuance-server + migrations).
- **`FSC_POSTGRES_PASSWORD`** in `fsc-infra/.env` — shared password for all FSC-infra database users (three orgs × controller/manager/txlog + directory).

That covers the default (`make demo`, DvTP-only). For the wallet flow (`make demo-eudi` / `make demo-full`) you also need:

- **`NLWALLET_PATH`** in `.env` — path to a local checkout of the [nl-wallet](https://github.com/MinBZK/nl-wallet) repo. Used to build the issuance-server + demo-issuer binaries from source.
- **Three public HTTPS URLs** — `EUDI_PUBLIC_URL` (wallet reaches issuance-server), `EUDI_READER_ORIGIN_URL` (published as `requestOriginBaseUrl` in `reader_auth.json`; usually the same host as `EUDI_PUBLIC_URL`), and `EUDI_BRI_URL` (issuance-server reaches eudi-adapter). See [EUDI public reachability](#eudi-public-reachability) for the three supported options (own domain / bundled Cloudflare tunnel / ad-hoc tunnel).
- **Six EUDI crypto slots** in `.env` — `EUDI_READER_KEY/CERT`, `EUDI_ISSUER_KEY/CERT`, `EUDI_STATUS_KEY/CERT`. `make eudi-config` (auto-run by `make demo-eudi`) renders `services/eudi-issuance-server/config/{issuance_server.toml,reader_auth.json,...}` from their `.example` templates via `envsubst`. The `.example` files contain public trust-anchors and URL placeholders; **the 6 private keys/certs are not in the public repo** — request them out-of-band from the maintainer for a working wallet-QR flow. Requires `envsubst` (`brew install gettext` on macOS).

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
                      # Curl directly at pdp-service /evaluation for policy tests.

make demo-eudi        # Wallet flow only (~5-10 min first boot; PKI + FSC-infra + contract seed)
                      # Requires NLWALLET_PATH + two public HTTPS URLs
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
3. Open the **consumer mock** (`:9001`), enter the consent-id, click **"Run query"** — the consumer queries income data via HV-Outway → BD-Inway → AuthZen call to PDP → OPA → source sidecar (PI→BSN) → GraphQL.
4. Open the **developer portal** (`:9003`) → Use tab → click "Watch" → the live arch strip lights up hop by hop.
5. Revoke consent from the portal and repeat the query — OPA denies with `CONSENT_WITHDRAWN`.

## "Break things" Guide

### Edit OPA policies (Rego hot-reload)

OPA watches the `policies/` directory. Edit any Rego file and save — OPA reloads automatically.

```bash
# Example: force OPA to deny everything
echo 'package dvtp.authz
import rego.v1
default allow := false
default reason := "policy_override"' > policies/dvtp/authz.rego

# Run a query — the deny surfaces in the consumer UI and the developer portal.

# Restore the original policy
git checkout policies/dvtp/authz.rego
```

### Revoke consent

Click "Revoke consent" in the consent portal (`:9002`), repeat the query. OPA reads the consent register, sees status=REVOKED → DENY.

### View OPA decision logs

```bash
docker compose logs -f opa
```

## Service ports

| Service | Port | Description | Real/Mock |
|---------|------|-------------|-----------|
| Consumer mock | 9001 | Consumer UI (React/Vite) | Demo frontend |
| Consent portal | 9002 | Citizen UI (React/Vite) | Demo frontend |
| Developer portal | 9003 | Architect inspection (React/Vite) | Demo frontend |
| dev-portal-backend | 9407 | Trace hub + explain endpoint | Real (Go) |
| GraphQL Server | 9400 | Sample source with income data | Real (Go) |
| pdp-service | 9408 | AuthZen endpoint behind FSC-Inway (P3 context handler) | Real (Go) |
| bron-sidecar | 9409 | Source-side gateway; PI→BSN via BSNk (subject_id_type-driven) | Real (Go) |
| Consent Register | 9402 | Consent store (PIP) | Mock (Go, in-memory) |
| BSNk Mock | 9403 | Pseudonymization service | Mock (Go, deterministic) |
| HV-Manager UI | 8096 | Consumer-org FSC-Controller (mortgage-lender demo org) | Real (OpenFSC v2.4.0) |
| EDI-Manager UI | 8094 | Consumer-org FSC-Controller (EUDI issuer) | Real (OpenFSC v2.4.0) |
| BD-Manager UI | 8092 | Provider-org FSC-Controller (source-holder demo org) | Real (OpenFSC v2.4.0) |
| OPA | 9181 | Policy Decision Point (Rego) | Real |
| Jaeger | 9686 | Distributed tracing UI | Real |
| OTel Collector | 9317 | Trace collection | Real |

## What is real vs. demo scaffolding

| Component | Status | Notes |
|-----------|--------|-------|
| OPA / Rego policies | **Real** | Production OPA container with real Rego evaluation |
| OpenTelemetry + Jaeger | **Real** | Production-grade distributed tracing |
| GraphQL Server | **Real** | Real Go GraphQL implementation |
| FSC (Manager/Inway/Outway/Controller/txlog) | **Real** | OpenFSC v2.4.0 upstream containers, three orgs (consumer, EUDI-issuer, provider) each with their own PostgreSQL + certs |
| pdp-service | **Real** | AuthZen endpoint behind FSC-Inway; the only policy endpoint for both flows |
| bron-sidecar | **Real** | Source-side gateway; PI→BSN driven by the `subject_id_type` grant-property |
| Consent Register | **Mock** | In-memory; production would be a persistent store |
| BSNk Mock | **Mock** | Deterministic SHA-256; real BSNk uses ElGamal on elliptic curves |

## Architecture

The five-factor authorization model demonstrated:

| # | Factor | Implementation in demo |
|---|--------|------------------------|
| ① | Org identity (mTLS) | FSC-Manager validates peer-certs; FSC-Inway includes peer_cert_chain in the AuthZen context |
| ② | Org permission (JWT) | FSC-Manager issues `Fsc-Authorization` with grant + `Properties.{flow, subject_id_type}` |
| ③ | Access basis (consent) | pdp-service fetches consent via `GET /consents?pi=<pi>&scope=...` on consent-register |
| ④ | Data scope (GraphQL) | OPA checks requested fields against the dienstencatalogus (rules DVT0001/EUD0001) |
| ⑤ | Request validity | OPA validates `pip.consent` + `resource.pi` binding + expiry |

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

- **Consumer-org (mortgage lender)** — consent-flow consumer with contract `flow=dvtp:query`, `subject_id_type=pseudonym`
- **EDI-Issuer** — wallet-flow consumer with contract `flow=eudi:attestation`, `subject_id_type=direct`
- **Provider (source-holder)** — provides the `bri` service; endpoint routes through the bron-sidecar

`make demo` orchestrates the full sequence automatically:
- PKI generation (root-CA + per-org certs)
- FSC-infra start (three orgs + directory-peer)
- Contract seed (bri-service + publication + two connection contracts + grant-links)
- Main stack with dienstverlener-backend, eudi-adapter, pdp-service, bron-sidecar

Step-by-step targets are available for debugging:

1. **`make fsc-all-up`** — FSC-infra + orgs. The directory-manager runs with `--auto-sign-grants=servicePublication`; the provider-manager runs with `--auto-sign-grants=serviceConnection`. Contracts reach `CONTRACT_STATE_VALID` without manual review.

2. **`make fsc-seed-bri`** + **`bash fsc-infra/scripts/seed-bri-connection-hv.sh`** — services + contracts + grant-links. Registers the `bri` service in the provider-Controller (endpoint = bron-sidecar), posts publication + two connection contracts (with grant-properties `flow` + `subject_id_type`), upserts the grant-links per consumer. Idempotent.

   Grant-link upsert goes via direct SQL — v2.4.0 has no REST endpoint for grant-link CRUD.

3. **Generate pdp-service TLS cert + restart**:
   ```bash
   bash fsc-infra/pki/generate-pdp-cert.sh
   docker compose up -d --force-recreate dienstverlener-backend eudi-adapter pdp-service graphql-server bron-sidecar
   docker compose -f fsc-infra/docker-compose.yml up -d --force-recreate bd-inway
   ```

   `generate-pdp-cert.sh` produces a self-signed cert (SAN=`pdp-service`) — FSC-Inway's AuthZen plugin requires HTTPS+CA. The same `.pem` is mounted by the provider-inway as `AUTHZEN_ROOT_CA`.

**Reset**:

```bash
make fsc-down
docker volume rm fsc-infra_postgres-data    # wipe all contracts/publications
```

Without a volume wipe, contracts and grant-links survive a restart.

## EUDI public reachability

The EUDI flow needs two publicly-reachable HTTPS URLs:

- `EUDI_PUBLIC_URL` — the wallet on a phone opens this to talk to the `issuance-server`.
- `EUDI_BRI_URL` — the `issuance-server` fetches attestations from the `eudi-adapter` at this URL.

Both values are read from `.env`. Pick whichever way to expose the two services fits your setup:

**(a) Own domain / reverse proxy** — point two HTTPS hostnames at the compose ports and set the URLs. Nothing else to install.

**(b) Cloudflare named tunnel (bundled)** — one Cloudflare tunnel with two Public Hostnames configured in the dashboard, plus the connector token in `.env`:

```bash
# In .env
CLOUDFLARE_TUNNEL_TOKEN=eyJ...
EUDI_PUBLIC_URL=https://eudi-is.your-cf-hostname.tld/
EUDI_BRI_URL=https://eudi-bri.your-cf-hostname.tld/

# Start the tunnel alongside the EUDI stack
docker compose --profile eudi --profile cloudflare-tunnel up -d
```

**(c) Ad-hoc tunnel** (ngrok, `cloudflared --url`, `tailscale funnel`, …) — start it yourself, paste the two URLs into `.env`, then bring up the stack without the `cloudflare-tunnel` profile.

## Testing

```bash
# Go happy-path integration tests (per service)
for svc in bron-sidecar bsnk-mock consent-portal-backend consent-register \
           dev-portal-backend dienstverlener-backend eudi-adapter \
           graphql-server pdp-service sector-pip; do
  (cd services/$svc && go test -timeout 60s ./...)
done

# OPA policy unit tests
docker compose exec opa opa test /policies -v
```

CI (`.github/workflows/ci.yml`) runs both on every PR.

## Troubleshooting

**Services not starting?**
```bash
docker compose logs <service-name>
```

**OPA returning unexpected results?**
```bash
# Check OPA decision logs
docker compose logs opa | grep "decision"

# Test OPA directly
curl -X POST http://localhost:9181/v1/data/dvtp/authz -d '{"input": {...}}'
```

**Frontend not loading?**
- Check the three frontends (`dienstverlener-mock` :9001, `toestemmingsportaal-frontend` :9002, `developer-portal` :9003) and their backends.
- `docker compose logs <service>` for the container in question.

## Adding new access flows

The architecture is designed for incremental extension. Every new flow shares the same FSC-Inway → pdp-service (AuthZen) → OPA → bron-sidecar → GraphQL chain — only the policy rules, contract properties, and entry points differ.

- **Legal-basis (gov-to-gov)**: add `policies/legal-basis/*.rego`, add a new FSC consumer org with contract `flow=g2g:legal-basis`, the PDP dispatches on the token property.
- **Wallet flow (already implemented)**: see `eudi-adapter`.
- **AS4 / SDG-OOTS**: add an AS4 bridge mock + Domibus mock.

## Repository owner

Owner: **Jeroen de Kok** — <jeroen.dekok@ictu.nl>

Please open an issue for questions or feature requests. For security issues see [SECURITY.md](SECURITY.md).
