# GBO — Gemeenschappelijke Bronontsluiting

This repository is a runnable reference demo for GBO. It shows how a consumer
can request data from a source, normally over OpenFSC with every request
evaluated by an OpenFTV policy before the source returns data. The EUDI demo
also contains one deliberately unsecured HTTP source to prove that onboarding
is not technically coupled to FSC; that source is not a security example.

The unsecured source is mounted only by the local Compose setup and is not
part of the deployable source configuration used by simulation environments.

The demo contains two ways to start that request:

- **DvTP consent** — a citizen grants a consumer permission to request a
  specific data scope.
- **EUDI wallet issuance** — a citizen shares a PID from a wallet and receives
  a source-backed credential.

The production-oriented flows use the same core path:

```text
consumer → FSC Outway → FSC Inway → OpenFTV PDP → source sidecar → GraphQL source
```

## Requirements

For every mode:

- Docker with the Compose plugin;
- Git, Make, Bash, `curl`, `jq`, OpenSSL and Python 3.9 or newer;
- a local `.env` copied from `.env.example`;
- non-empty `EUDI_POSTGRES_PASSWORD`, `SOURCE_REGISTRY_PASSWORD` and
  `SOURCE_REGISTRY_READER_PASSWORD` values in `.env`.

The complete EUDI flow additionally requires:

- the pinned nl-wallet v0.5.0 submodule;
- two public HTTPS URLs: one for the issuance server and one for the EUDI
  adapter;
- a compatible test wallet with a valid `urn:eudi:pid:nl:1` credential;
- issuer and reader trust anchors already trusted by that wallet.

The test persona that works with all included wallet offers has mock BSN
`999991772`. The repository configures the issuance side; it does not install
the wallet app or issue its PID.

## Quick start

Clone the repository with its nl-wallet submodule and create the local
configuration:

```bash
git clone --recurse-submodules https://github.com/ICTU/GBO-demo.git
cd GBO-demo
cp .env.example .env
```

Set the required values in `.env`:

```dotenv
EUDI_PUBLIC_URL=https://eudi-is.example.org/
EUDI_BRI_URL=https://eudi-bri.example.org/
EUDI_POSTGRES_PASSWORD=<local-random-password>
SOURCE_REGISTRY_PASSWORD=<local-random-writer-password>
SOURCE_REGISTRY_READER_PASSWORD=<local-random-reader-password>
```

Before starting an EUDI demo, provide the issuer and reader CA material that
the test wallet already trusts. The setup deliberately never generates trust
anchors. By default it loads these four files from
`.local/secrets/development-ca/`:

```text
issuer-ca-key.pem
issuer-ca-cert.pem
reader-ca-key.pem
reader-ca-cert.pem
```

Keep the private keys outside version control. To load the same managed CA
material from another location, set `ONBOARDING_SECRETS_DIR` to the absolute
path of that secrets root; both the provisioning command and Compose use it.
Setup fails before onboarding when any required CA file is absent.

Those CA private keys are provisioning input, not application runtime input.
After the issuer, reader and status leaf certificates have been provisioned,
the source reconciler receives only their public certificates and the two
public CA certificates. Leaf private keys are mounted exclusively into the
issuance materializer. A Kubernetes runtime Secret must not contain
`issuer-ca-key.pem` or `reader-ca-key.pem`.

`EUDI_PUBLIC_URL` must route to the issuance server and its hostname must be in
the reader-certificate DNS SAN. `EUDI_BRI_URL` must route to the EUDI adapter.
See [source onboarding](docs/source-onboarding.md) for the local certificate
and activation model.

With an existing reverse proxy or externally managed tunnel, start everything
with:

```bash
make demo-full
```

To use the bundled Cloudflare connector, also set
`CLOUDFLARE_TUNNEL_TOKEN` and run:

```bash
COMPOSE_FILE=docker-compose.yml:docker-compose.cloudflare-tunnel.yml make demo-full
```

Configure these Cloudflare Public Hostnames:

| Public hostname | Tunnel service |
| --- | --- |
| Host from `EUDI_PUBLIC_URL` | `http://eudi-issuance-server:18001` |
| Host from `EUDI_BRI_URL` | `http://eudi-adapter:4009` |

Add a final hostname-wide **Bypass cache** rule for `EUDI_PUBLIC_URL` and purge
that hostname once. Issuer metadata and QR sessions must not be cached.

The first EUDI build can take several minutes. `make demo-full` performs the
complete idempotent bootstrap: FSC setup, contracts, source onboarding,
development certificates, EUDI configuration and application startup.

Open the demo at:

| Entry point | URL |
| --- | --- |
| Demo landing page | <http://localhost:9000> |
| Consumer mock | <http://localhost:9001> |
| Consent portal | <http://localhost:9002> |
| Developer portal | <http://localhost:9003> |

## Other modes

| Command | Starts |
| --- | --- |
| `make demo-minimal` | Core services and observability only |
| `make demo` | DvTP consent flow over OpenFSC |
| `make demo-eudi` | EUDI wallet flow over OpenFSC |
| `make demo-full` | DvTP and EUDI flows together |
| `make demo-manager` | Core services with OpenFTV Manager policy distribution |
| `make demo-down` | Stops the application and FSC stacks |

`make demo-manager` needs the additional variables documented in
[its component README](services/openftv-manager-ui/README.md). Prefer the
`demo-*` targets over starting Compose directly; they create certificates,
contracts and generated configuration in the required order.

## Walkthroughs

### DvTP consent

1. Open the [consent portal](http://localhost:9002) and sign in with a mock
   citizen from `services/graphql-server/mockdata/citizens.json`.
2. Grant a consumer consent for a scope such as `bd:ib:2025`.
3. The portal returns a signed consent token to the
   [consumer mock](http://localhost:9001), which immediately runs the query.
4. Open the [developer portal](http://localhost:9003) to inspect the policy
   decision, trace and FSC transaction.
5. Revoke the consent and repeat the query. The PDP now denies it with
   `CONSENT_WITHDRAWN`.

The developer portal injects `DVTP_CONSUMER_PEER_ID` into the
`dienstverlener_oin` field of its predefined DvTP issuance scenarios. It uses
`99999999900000000300` for local development when the variable is unset.
Deployments must configure the FSC Peer ID of their actual consumer. Custom
issuance payloads and user-saved scenarios keep the value entered by the user.

See [the DvTP consent architecture and sequence diagrams](docs/consent-flow.md)
for the component boundaries and the grant, use and revocation flows.

`S01` is the architecture role identifier used in the demo diagrams for the
consent-register; it is not a separate service. The demo consent token is a
bearer JWT. The consent-register generates an ephemeral P-256 key
when `CONSENT_SIGNING_KEY_PATH` is empty, matching its default in-memory
consent store. A persistent deployment must provide a stable PKCS#8 or SEC1
P-256 private key and set an explicit `CONSENT_SIGNING_KEY_ID`. The demo JWKS
exposes only the current key; production rotation must retain old public keys
until their tokens expire. Production hardening should also shorten the token
lifetime and bind proof of possession
to the FSC/mTLS identity (for example with a confirmation claim); the current
explicit `dienstverlener_oin` check prevents cross-consumer use but does not
make a stolen bearer token non-replayable by that same consumer.

Because the token returns through the browser, the consent portal only accepts
`http` or `https` return URLs whose exact origin occurs in
`VITE_ALLOWED_RETURN_ORIGINS` (comma-separated). Compose derives the demo value
from `DIENSTVERLENER_PUBLIC_URL`; production images must supply the same value
as a build argument.

### EUDI wallet issuance

1. Start `make demo-full` or `make demo-eudi` and open the
   [landing page](http://localhost:9000).
2. Select income 2024, income 2025, the BRP/RvIG death certificate or the
   explicitly labelled unsecured demo credential.
3. Scan the newly generated QR code with the compatible test wallet.
4. Share the PID for mock BSN `999991772`, review the credential preview and
   accept issuance.
5. Inspect the resulting policy decision and trace in the
   [developer portal](http://localhost:9003).

QR sessions are stateful. Generate and scan a new QR after restarting the
issuance server or changing activated source metadata.

## How the demo works

The DvTP and FSC-backed EUDI entrypoints share transport, policy evaluation,
identifier resolution and source access. Belastingdienst and BRP/RvIG are
separate logical sources with their own metadata, data services, policies and
certificate sets; this demo publishes both through one FSC participant. The
EUDI adapter contains no hard-coded source or offer catalog: active source
metadata generates the issuance-server products and the frontend offer list.

The demo combines production-grade components with deliberate test doubles:

| Area | In this repository |
| --- | --- |
| Transport | OpenFSC managers, controllers, Inways and Outways |
| Authorization | OpenFTV PDP with OPA/Rego policies |
| Source access | Go GraphQL services behind source-side sidecars |
| Observability | OpenTelemetry, Jaeger, Loki and Grafana |
| Processing logs | `ldv-logboek` per Verantwoordelijke — Belastingdienst, RvIG and GBO (LDV v1.0.0), alongside the PDP's ADL and FSC-Logging |
| Identity and data | Synthetic citizens, deterministic BSNk and mock DigiD |
| Consent | In-memory demo register |

This is a reference architecture, not a production-ready deployment. In the
EUDI demo, the disclosed subject is not yet independently cryptographically
bound to the selected source record before policy evaluation. See
[SECURITY.md](SECURITY.md) for the security boundary and known limitations.

## Testing

CI runs Go linting and tests, Rego validation and tests, frontend type checks,
Helm validation and Docker builds. For a focused local change, run the same
check in the affected component, for example:

```bash
(cd services/eudi-adapter && go test -timeout 60s ./...)

docker run --rm -v "$(pwd)/policies:/w" -w /w \
  openpolicyagent/opa:1.9.0-static test /w -v
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the pull-request checklist.

## Troubleshooting

Start by checking container state and logs:

```bash
docker compose ps
docker compose logs --tail=200
```

Common fixes:

- use `make demo-full` or the matching `demo-*` target instead of starting
  individual containers;
- after changing source metadata, rerun `make onboard-demo-sources`; the
  adapter observes the promoted release live. Run `make eudi-config` to
  rematerialize and restart the issuance-server;
- scan a fresh QR after a restart or metadata change;
- if Cloudflare reports `HIT` or an increasing `Age` for issuer metadata,
  correct the cache-bypass rule and purge the issuance hostname;
- use `make demo-down` to stop both stacks, or `make fsc-clean` only when you
  intentionally want to remove disposable local FSC state.

The detailed diagnosis for port conflicts, certificates, FSC contracts,
source activation, wallet trust and cached QR sessions is in
[TROUBLESHOOTING.md](TROUBLESHOOTING.md).

## Further reading

- [DvTP consent architecture and flow](docs/consent-flow.md)
- [Source configuration and onboarding](docs/source-onboarding.md)
- [Source metadata cache and Type Metadata](docs/source-metadata-cache.md)
- [`gbo-simple-v1` mapping profile](docs/gbo-simple-v1.md)
- [Logboek Dataverwerkingen](services/ldv-logboek/README.md)
- [Observability](OBSERVABILITY.md)
- [Security](SECURITY.md)
- [Contributing](CONTRIBUTING.md)
- [Changelog](CHANGELOG.md)

GBO is maintained by the Dutch Ministry of the Interior and Kingdom Relations
(BZK), Digital Government Directorate, with ICTU as technical steward. See
`publiccode.yml` for repository metadata and contacts.
