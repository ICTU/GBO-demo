# GBO — Gemeenschappelijke Bronontsluiting

This repository is a runnable reference demo for GBO. It shows how a consumer
can request data from a source, normally over OpenFSC with every request
evaluated by an OpenFTV policy before the source returns data. The EUDI demo
also contains one deliberately unsecured HTTP source to prove that onboarding
is not technically coupled to FSC; that source is not a security example.

The unsecured source runs only locally, with the EUDI profiles, and is not
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

- the [Aspire CLI](https://aspire.dev) 17 and Go 1.26 or newer (the AppHost
  is written in Go);
- Docker (Docker Desktop, OrbStack or Docker Engine);
- Node.js 24 or newer with npm 11, for the Vite frontends; it must be the
  first `node` on PATH (the AppHost checks and says where a newer one is);
- Go 1.26 or newer, for the host-side certificate steps;
- Git, Bash, `curl`, `jq` and OpenSSL; Python 3.9 or newer for the OpenFTV
  Manager seed.

The demo's database passwords are generated on the first run and kept in
the AppHost's user secrets (`aspire secret list` in `aspire/`).

The complete EUDI flow additionally requires:

- the pinned nl-wallet v0.5.0 submodule;
- Go 1.26 or newer on the host (the AppHost needs it anyway): the AppHost's
  provisioning steps and `aspire/scripts/provision.sh` sign with `go run` on
  the host, so the CA keys never enter a container;
- two public HTTPS URLs: one for the issuance server and one for the EUDI
  adapter (the own credential issuer and the ASI route need more; see
  [the port table](#public-urls-and-ports));
- a compatible test wallet with a valid `urn:eudi:pid:nl:1` credential;
- issuer and reader trust anchors already trusted by that wallet.

The test persona that works with all included wallet offers has mock BSN
`999991772`. The repository configures the issuance side; it does not install
the wallet app or issue its PID.

## Quick start

Clone the repository with its nl-wallet submodule:

```bash
git clone --recurse-submodules https://github.com/ICTU/GBO-demo.git
cd GBO-demo
```

Start the DvTP demo from the AppHost directory. It needs no public URLs,
trust anchors or wallet:

```bash
cd aspire
aspire run
```

The [AppHost](aspire/apphost.go) builds every image, creates the FSC
certificates and contracts and starts the demo in dependency order. The
first run takes a few minutes. The Aspire dashboard
(<http://localhost:15126>) lists every resource with its state, logs and
URLs, and holds the traces, logs and metrics. Open the demo at:

| Entry point | URL |
| --- | --- |
| Demo landing page | <http://localhost:9000> |
| Consumer mock | <http://localhost:9001> |
| Consent portal | <http://localhost:9002> |
| Developer portal | <http://localhost:9003> |
| Aspire dashboard (traces, logs, resources) | <http://localhost:15126> |

Then follow the [DvTP consent walkthrough](#dvtp-consent).

Stop it with Ctrl+C. See [aspire/README.md](aspire/README.md) for the
profiles, settings and secrets.

The AppHost publishes its ports on localhost. To open the demo from another
machine, put a reverse proxy in front (next section) and set the host name
that browser uses, then start again:

```bash
aspire run -- --Gbo:PublicHost=demo.example.org
```

### HTTPS behind a reverse proxy

Off localhost, some pages need https: the OpenFTV Manager login and the
reference QTSP. Put a TLS-terminating reverse proxy (for example
`tailscale serve` or Caddy) in front of each port the browser opens, on the
same port number, forwarding to `http://127.0.0.1:<port>`. It must pass
WebSocket upgrades and `X-Forwarded-*` headers. Then start again with the
public host and scheme:

```bash
aspire run -- --Gbo:PublicHost=demo.example.org --Gbo:PublicScheme=https
```

The plain http ports stay on the loopback interface, so only the proxy
reaches them.

For the EUDI flows some containers call the public URLs themselves
(`eudi-bri-url` and `eudi-trust-public-url`), so those hostnames must also
resolve to the proxy, and the proxy must be reachable, from inside the
containers.

### The complete demo, with EUDI

The `full` profile adds the EUDI wallet flow to DvTP. It additionally needs
two public URLs, which the AppHost asks for as parameters. Set them once:

```bash
aspire secret set Parameters:eudi-public-url https://eudi-is.example.org/
aspire secret set Parameters:eudi-bri-url https://eudi-bri.example.org/
```

#### Public URLs and ports

Each public URL routes to one published host port. The `full` profile needs
the first two; the others serve the
[own credential issuer](#own-credential-issuer-for-eudi-wallets) and the
ASI route (`Gbo__Profiles=full,asi`). Set the ones you use: parameters with
`aspire secret set Parameters:<name> <url>`, settings with
`aspire secret set Gbo:<name> <url>` (or after `--`), and the
`provision.sh` variables as `NAME=value` arguments of that script.

| Setting | Host port | Needed for |
| --- | --- | --- |
| `Parameters:eudi-public-url` | 18001 | NL Wallet issuance (issuance server) |
| `Parameters:eudi-bri-url` | 9409 | EUDI adapter: the issuance server's data source and type metadata |
| `Parameters:issuer-public-url` (and `Gbo:IssuerPublicUrl` for the portal) | 9429 | own credential issuer |
| `Parameters:eudi-trust-public-url` | 9430 | trust host: LoTE lists, CRL, status lists |
| `Gbo:AsiProviderPublicUrl` | 9424 | ASI provider |
| `Gbo:AsiAuthorizationServerPublicUrl` | 9425 | ASI login and consent in the browser |
| `Gbo:QtspPublicUrl` | 9427 | reference QTSP |
| `ASI_WALLET_VERIFIER_PUBLIC_URL` (`provision.sh asi-wallet-verifier`) | 9428 | NL Wallet PID presentation in the ASI route |
| `ASI_EUDI_WALLET_VERIFIER_PUBLIC_URL` (`provision.sh asi-eudi-wallet-verifier`) | 9432 | EUDI wallet PID presentation in the ASI route |
| `Gbo:PublicHost` | 9000, 9003 | landing page, developer portal |

Never publish port 9431: it is the own issuer's internal offer API, which
has no authentication. Keep it out of the proxy.

Before starting an EUDI demo, provide the issuer and reader CA material that
the test wallet already trusts. The setup deliberately never generates trust
anchors, apart from the registrar root described below, which it creates only
on request. By default it loads these four files from
`.local/secrets/development-ca/`:

```text
issuer-ca-key.pem
issuer-ca-cert.pem
reader-ca-key.pem
reader-ca-cert.pem
```

These files are managed material and are not part of the repository.
Published wallet builds trust only these managed CAs; a CA of your own works
only with a wallet you build yourself against your own trust lists.

Keep the private keys outside version control. To load the same managed CA
material from another location, set `Gbo:SecretsDir` to the absolute path of
that secrets root (`aspire run -- --Gbo:SecretsDir=/path`); the AppHost and
`aspire/scripts/provision.sh` (`ONBOARDING_SECRETS_DIR`) both use it. When a
required CA file is absent, the AppHost leaves the EUDI parts out and starts
everything else; the `eudi-not-configured` step in the dashboard names the
missing files. The same holds for the `credential-issuer` profile without a
wallet registration.

Those CA private keys are provisioning input, not application runtime input.
After the issuer, reader and status leaf certificates have been provisioned,
the source reconciler receives only their public certificates and the two
public CA certificates. Leaf private keys are mounted exclusively into the
issuance materializer. A Kubernetes runtime Secret must not contain
`issuer-ca-key.pem` or `reader-ca-key.pem`.

The [GBO trust lists](docs/gbo-trust-lists.md) combine NL pre-production
anchors with the GBO roots in four role-specific LoTE files. The temporary
demo mode trusts the fixed official HTTPS source; configuring its public key
enables mandatory JWT signature verification. Internal PID consumers verify
our signed list against the independently provisioned LoTE root; the Go
services refresh on use hourly, and the upstream NL verifier uses a verified
provisioning snapshot plus restart.
See the [trust-host runbook](trust-host/README.md) for host-only signing and
publication.

The own credential issuer (`services/eudi-credential-issuer`, profile
`credential-issuer`; see [below](#own-credential-issuer-for-eudi-wallets))
also needs per source an access certificate (WRPAC) and a registration
certificate (WRPRC). `aspire/scripts/provision.sh development-registrar-root` creates the
registrar root that signs the registration certificates, once, in
`development-ca/` (`registrar-ca-key.pem`, `registrar-ca-cert.pem`); it is a
trust anchor of its own and is never replaced. Its certificate is copied to
`.local/public-certificates/development-ca/` for the wallets' trust lists.
`aspire/scripts/provision.sh wallet-registration SOURCE_ID=<source_id>` then issues the
WRPAC under the reader root and the WRPRC for the types of the active Source
Release, creates the source's key for encrypted credential requests once
(`request-encryption-key.pem`, kept on later runs), and writes the reader-root CRL and the WRPRC status list to
`.local/public-trust/` for the trust host. It reads the source from the
running Source Registry (start the AppHost with an EUDI profile first); the
signing runs on the host, so the CA keys never enter a container. It needs `EUDI_TRUST_PUBLIC_URL`
(the public root URL of that host, which goes into the certificates) and, the
first time for a source, `KVK_NUMBER`: the organizationIdentifier in the WRPAC
and the WRPRC is `NTRNL-<KvK number>`, because ETSI EN 319 412-1 section 5.1.4
defines no identity type for an OIN. The number is recorded per source; a
different one for a registered source is refused. The demo sources use
fictional numbers: `99990200` for `belastingdienst`, `99990300` for `rvig` and
`99990900` for `demo-unsecured`. The CRL and the status list are valid for a year
at most and never outlive the reader root (the pre-provisioned one expires on
2027-04-30); nothing renews them, and revocation is not supported: every run
publishes both afresh. The trust URL is recorded in
`development-ca/wallet-registration.json`; to change it, remove that file and
provision every source again.

For disclosure-based issuance (the wallet discloses the BSN from its PID,
then gets the offer), the same command also issues a WRPRC for the source as
a relying party (`wrprc-verifier.jwt`): entitlement `Service_Provider`,
authorizing the `bsn` of `urn:eudi:pid:nl:1`, with the purpose the wallet
shows and a status entry of its own. Run it again for sources provisioned
before, and `aspire/scripts/provision.sh test-pid` too (it now also puts the test PID
root next to its registration); the materializer refuses a source without
them. It turns every offer
of the source's activated types into a disclosure offer under the same id,
and trusts the PIDs of the NL PID issuer CAs in `PID_TRUST_ANCHORS_FILE`
(default: the pre-production CA in
`services/asi-wallet-verifier/pid-issuer-anchors.txt`) and, once
provisioned, of the test PID.

`aspire/scripts/provision.sh qtsp-wallet-registration` registers the
reference QTSP as a provider of the own issuer, after the AppHost's
`provision-qtsp-issuance-certificates` step has run: a WRPAC and a WRPRC with the
`QEAA_Provider` entitlement for its test statement (fictional KvK number
`99991300`, not a public body). The materializer then adds the QTSP, with
its statement valid for 30 days and its internal HTTPS callback as
bronpoort, trusted through the development issuer CA that signs the
callback's certificate; without the registration it leaves the QTSP out.
Next to `nl` and `eudi` the issuer then also has the combined profile
`wallet`, under which one offer serves both wallets; the NL Wallet joins it
once its wallet providers are mounted.

`aspire/scripts/provision.sh test-pid` adds a test PID provider to the own issuer: a PID
of the NL type (`urn:eudi:pid:nl:1`) for wallets without a real one, such as
the EUDI reference wallet, so the disclosure-based route can be shown with
them. Its credentials chain to a development root of its own
(`.local/test-pid-root/`, outside every container mount, created once, its
certificate copied to `.local/public-certificates/development-ca/`), which only the demo's wallet
builds and verifier trust. It is registered like a source, with the
`PID_Provider` entitlement and the fictional KvK number `99990100`, and fed by
`test-pid-source` from the BRP mock persons. It is a test fixture, not a PID
Provider.

`EUDI_PUBLIC_URL` must route to the issuance server and its hostname must be in
the reader-certificate DNS SAN. `EUDI_BRI_URL` must route to the EUDI adapter.
See [source onboarding](docs/source-onboarding.md) for the local certificate
and activation model.

With an existing reverse proxy or externally managed tunnel, start everything
with:

```bash
Gbo__Profiles=full aspire run
```

To use the bundled Cloudflare connector, set its token once and start with
the tunnel on:

```bash
aspire secret set Parameters:cloudflare-tunnel-token <token>
Gbo__Profiles=full aspire run -- --Gbo:CloudflareTunnel=true
```

Configure these Cloudflare Public Hostnames:

| Public hostname | Tunnel service |
| --- | --- |
| Host from `EUDI_PUBLIC_URL` | `http://eudi-issuance-server:18001` |
| Host from `EUDI_BRI_URL` | `http://eudi-adapter:4009` |

Add a final hostname-wide **Bypass cache** rule for `EUDI_PUBLIC_URL` and purge
that hostname once. Issuer metadata and QR sessions must not be cached.

The first EUDI build can take several minutes: the issuance server is built
from the nl-wallet sources. The AppHost performs the complete idempotent
bootstrap: FSC setup, contracts, source onboarding, development
certificates, EUDI configuration and application startup.

### Own credential issuer for EUDI wallets

The own credential issuer runs beside the issuance server, under the profile
`credential-issuer`. It needs the parameters `issuer-public-url` and
`eudi-trust-public-url` (its database password is generated), and the trusted
wallet providers in `.local/wallet-roots` (`Gbo:WalletRootsDir`). The AppHost
leaves it out until the trust lists are published, so with `full` running
(`Gbo__Profiles=full aspire run`), run in this order from the repository
root:

```bash
# The registrar root is created once and never replaced; skip it on a rerun.
test -f .local/secrets/development-ca/registrar-ca-key.pem \
  || aspire/scripts/provision.sh development-registrar-root
aspire/scripts/provision.sh wallet-registration SOURCE_ID=belastingdienst KVK_NUMBER=99990200
aspire/scripts/provision.sh wallet-registration SOURCE_ID=rvig KVK_NUMBER=99990300
aspire/scripts/provision.sh wallet-registration SOURCE_ID=demo-unsecured KVK_NUMBER=99990900
aspire/scripts/provision.sh test-pid ISSUER_PUBLIC_URL=https://eudi-issuer.example.org
aspire/scripts/provision.sh qtsp-wallet-registration
aspire/scripts/provision.sh trust-list-signer
aspire/scripts/provision.sh publish-trust-lists EUDI_TRUST_PUBLIC_URL=https://eudi-trust.example.org
aspire/scripts/provision.sh asi-eudi-wallet-verifier EUDI_TRUST_PUBLIC_URL=https://eudi-trust.example.org
```

Then restart with the issuer, the trust host and the ASI route
(`Gbo__Profiles=full,credential-issuer,asi aspire run` in `aspire/`). The
trust host must serve the lists before the issuer's materializer and the EUDI
wallet verifier fetch the PID list from it; the AppHost starts the trust host
alongside the issuer without waiting for it, so if
`credential-issuer-materialize` fails on the PID list, check that the four
`<eudi-trust-public-url>/lote/*.jwt` return 200 and restart it from the
dashboard. The LoTE signer keys stay in `.local/lote-signer/`
(`GBO_LOTE_SIGNER_DIR`); losing them invalidates every wallet build that
embeds their root. See the [trust-host runbook](trust-host/README.md) for the
publication details.

## Other modes

A profile picks which parts run, as the compose profiles did. Pick one with
`Gbo__Profiles`, comma-separated:

| Command (in `aspire/`) | Starts |
| --- | --- |
| `Gbo__Profiles=core aspire run` | Core services only: sources, PDP, logbooks, developer portal |
| `aspire run` (or `Gbo__Profiles=dvtp aspire run`) | DvTP consent flow over OpenFSC |
| `Gbo__Profiles=eudi aspire run` | EUDI wallet flow over OpenFSC |
| `Gbo__Profiles=full aspire run` | DvTP and EUDI flows together |
| `Gbo__Profiles=dvtp,integrator aspire run` | DvTP plus the integrator path |
| `Gbo__Profiles=full,asi aspire run` | Everything plus the ASI route |
| `Gbo__Profiles=manager aspire run` | Core services with OpenFTV Manager policy distribution |

Stop with Ctrl+C. `aspire/scripts/fsc-clean.sh` wipes the FSC certificates
and contracts for a fresh bootstrap. One-off host provisioning (wallet
registration, trust lists, test PID, wallet verifiers) is
`aspire/scripts/provision.sh`; see [aspire/README.md](aspire/README.md).

## Walkthroughs

### DvTP consent

The flow starts at the consumer, not at the consent portal: the portal only
shows consents that already exist.

1. Open the [consumer mock](http://localhost:9001) and click
   **Inloggen met DigiD**. The consumer sends you to the consent portal with
   its request: service, purpose and the scopes `bd:ib:2025` and `bd:ib:2024`.
2. Pick any sign-in method. The DigiD mock takes any username and a mock BSN
   as the password, for example `123456789`; the mock citizens are in
   `services/graphql-server/mockdata/citizens.json`.
3. Choose the data sets to share and click **Geef toestemming**. The portal
   returns a signed consent token to the consumer mock, which immediately runs
   the query and shows the income data.
4. Open the [developer portal](http://localhost:9003). **History · Replay**
   lists the issuance and the query with their outcome and trace id.
5. In another tab, open the [consent portal](http://localhost:9002), sign in
   as the same citizen, open the consent and click **Toestemming intrekken**.
   Back in the consumer mock, expand **Inkomensgegevens** and click
   **↻ Verversen** to repeat the query with the same token. The PDP now denies
   it. The consumer is told only `FIELD_NOT_PERMITTED`, so the consumer mock
   offers to consent again without naming a cause. The developer portal's
   history lists the request as `DENY`; the field's own reason,
   `CONSENT_WITHDRAWN`, is in the PDP decision log: the developer portal's
   decision view, or the `openftv-pdp` logs in the Aspire dashboard.

Starting again from the consumer mock's home page begins a new consent; it
does not repeat the query, because the token only lives in that result page.

The developer portal injects `DVTP_CONSUMER_PEER_ID` into the
`dienstverlener_oin` field of its predefined DvTP issuance scenarios. It uses
`99999999900000000300` for local development when the variable is unset.
Deployments must configure the FSC Peer ID of their actual consumer. Custom
issuance payloads and user-saved scenarios keep the value entered by the user.

See [the DvTP consent architecture and sequence diagrams](docs/consent-flow.md)
for the component boundaries and the grant, use and revocation flows.

A service provider can also query through an integrator: a processor with an
FSC peer of its own that connects on the provider's behalf over a delegated
connection. The consent then binds the provider, the FSC delegator, and the
integrator needs a mandate in the DvTP onboarding register. The demo uses the
direct path by default; the `integrator` profile adds the integrator
(`Gbo__Profiles=dvtp,integrator aspire run`). See [docs/integrator.md](docs/integrator.md).

A qualified trust service provider (QTSP) can verify attributes of a citizen
against the BRP through the ASIP mock of the ETSI TS 119 478 route
(`services/asi-provider`, `services/asi-authorization-server`). The ASIP is an
FSC consumer peer of its own (...1200). The `asi` profile adds it, next to
`eudi` or `full` (`Gbo__Profiles=full,asi aspire run`); a stub of the Discover Interface
(`services/asi-discovery-stub`) finds its data services per attribute, and a
reference QTSP (`services/qtsp-reference`, http://localhost:9427) is where a
user starts. The developer portal's ASI tab lays a request down at that QTSP,
with scenarios for each verification result, and shows the outcome next to
the chain. After a successful result, the QTSP offers a clearly marked,
non-qualified test statement to the same citizen's NL Wallet. The existing
central issuance server uses a separate QTSP usecase and certificate set and
fetches the pending statement over internal HTTPS. See
[services/asi-provider/README.md](services/asi-provider/README.md), and
[docs/asi-route.md](docs/asi-route.md) for what in this route is real,
stubbed or a GBO interpretation.

`S01` is the architecture role identifier used in the demo diagrams for the
consent-register; it is not a separate service. The demo consent token is a
bearer JWT. The consent-register keeps consents in memory with
`CONSENT_STORE=memory` (the compose demo) and in PostgreSQL with
`CONSENT_STORE=postgres` and `DATABASE_URL`; without `CONSENT_STORE` it
uses PostgreSQL when `DATABASE_URL` is set. It generates an ephemeral P-256
key when `CONSENT_SIGNING_KEY_PATH` is empty, which only the in-memory store
allows. A persistent deployment must provide a stable PKCS#8 or SEC1 P-256
private key. The key ID is `CONSENT_SIGNING_KEY_ID` when set, else the key's
RFC 7638 JWK thumbprint, so a new key always gets a new key ID and the PDP
fetches the JWKS again instead of verifying with a cached old key. The demo JWKS
exposes only the current key; production rotation must retain old public keys
until their tokens expire. Production hardening should also shorten the token
lifetime and bind proof of possession
to the FSC/mTLS identity (for example with a confirmation claim); the current
explicit `dienstverlener_oin` check prevents cross-consumer use but does not
make a stolen bearer token non-replayable by that same consumer.

Because the token returns through the browser, the consent portal only accepts
`http` or `https` return URLs whose exact origin occurs in the configured
allow-list (comma-separated). The image reads it from `ALLOWED_RETURN_ORIGINS`
when the container starts; `VITE_ALLOWED_RETURN_ORIGINS` only applies to the
Vite dev server, where Compose derives it from `DIENSTVERLENER_PUBLIC_URL` and
`KEYPER_PUBLIC_URL`.
Without either, the portal only accepts `http://localhost:9001` (a demo
shortcut), so a deployment must set `ALLOWED_RETURN_ORIGINS`.

### EUDI wallet issuance

1. Start the `full` or `eudi` profile and open the
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
| Authorization | OpenFTV v0.1.0 PDP with OPA/Rego policies |
| Source access | Go GraphQL services behind source-side sidecars |
| Observability | OpenTelemetry and the Aspire dashboard locally; Jaeger and Loki in the simulation deployment |
| Processing logs | `ldv-logboek` per Verantwoordelijke — Belastingdienst, RvIG and GBO (LDV v1.0.0), alongside the PDP's ADL and FSC-Logging |
| Identity and data | Synthetic citizens, deterministic BSNk and mock DigiD |
| Consent | In-memory demo register |

This is a reference architecture, not a production-ready deployment. In the
EUDI demo, the disclosed subject is not yet independently cryptographically
bound to the selected source record before policy evaluation. See
[SECURITY.md](SECURITY.md) for the security boundary and known limitations.

### What a denial tells the citizen

A denied request carries the code the policy gives the consumer, following the
FTV GraphQL profile: it says that the request was refused, never why or which
field. The reason per field stays with the PDP and the developer portal.

The disclosure decision sits in one place, on the server:
`services/dienstverlener-backend/consumer/denial.go` maps the upstream reason onto a
`denial_code`. Only `FIELD_NOT_PERMITTED` passes through — a refused field is
the one denial the citizen can act on, by giving consent again. Everything
else, including `ACCESS_DENIED`, an unrecognised code and every transport
failure, becomes `UNAVAILABLE`.

The consumer frontend renders that code and nothing else
(`dienstverlener-mock/src/lib/denialMessage.ts`). It never reads `reason`,
which stays technical text for logs and the developer portal. A code the
frontend does not know falls through to the generic message rather than being
shown, so a code added to the policy later cannot surface on a citizen's
screen by default.

The citizen screen shows the trace-id, which is something to quote to a
helpdesk, and not the reason code, which is operator detail. The developer
portal is where the full decision, its reason and the policy path are shown.

One caveat while running this demo: the FSC Inway drops the PDP's reason
before it reaches the consumer ([OpenFSC #308](https://gitlab.com/rinis-oss/fsc/open-fsc/-/issues/308)),
so until that fix lands upstream every denial degrades to the generic message.
The backend handles both cases; the specific messages appear once the Inway
forwards the reason.

## Testing

CI runs Go linting and tests, Rego validation and tests, frontend type checks,
Helm validation and Docker builds. For a focused local change, run the same
check in the affected component, for example:

```bash
(cd services/eudi-adapter && go test -timeout 60s ./...)

scripts/policy-check.sh --test   # opa check, format diff and Rego unit tests, same image as CI
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the pull-request checklist.

### Editing policies

The PDP under the AppHost loads `policies/` from disk and hot-reloads on
save. A module that does not compile is not rejected loudly: OpenFTV still
logs `policy added/replaced`, stays healthy and keeps answering with the
previous rules ([#151](https://github.com/ICTU/GBO-demo/issues/151)). A rule
whose change has no effect is therefore often a rule that never compiled. Run

```bash
scripts/policy-check.sh
```

after every edit. It reports a syntax error with file and line in a second or
two, without the stack running. The AppHost runs it before it starts the PDP
(the `policy-check` resource), so a broken module stops the start.

## Troubleshooting

Start in the Aspire dashboard (<http://localhost:15126>): it shows every
resource's state, the run-once steps' exit codes and each resource's console
logs. From a terminal in `aspire/`:

```bash
aspire describe                       # resources and their state
aspire logs <resource>                # one resource's console log
```

Common fixes:

- start the demo with a profile rather than individual containers: the
  AppHost orders certificates, contracts and configuration;
- after changing source metadata, restart `source-reconciler` from the
  dashboard; the adapter observes the promoted release live. Restart
  `eudi-issuance-materialize` and then `eudi-issuance-server` to
  rematerialize the issuance server's configuration;
- scan a fresh QR after a restart or metadata change;
- if Cloudflare reports `HIT` or an increasing `Age` for issuer metadata,
  correct the cache-bypass rule and purge the issuance hostname;
- stop with Ctrl+C; run `aspire/scripts/fsc-clean.sh` only when you
  intentionally want to remove disposable local FSC state.

The detailed diagnosis for port conflicts, certificates, FSC contracts,
source activation, wallet trust and cached QR sessions is in
[TROUBLESHOOTING.md](TROUBLESHOOTING.md).

## Further reading

- [DvTP consent architecture and flow](docs/consent-flow.md)
- [An integrator acting for a service provider](docs/integrator.md)
- [Source configuration and onboarding](docs/source-onboarding.md)
- [Source metadata cache and Type Metadata](docs/source-metadata-cache.md)
- [`gbo-simple-v1` mapping profile](docs/gbo-simple-v1.md)
- [How GBO implements the FTV GraphQL profile](docs/ftv-graphql-profile.md)
- [Logboek Dataverwerkingen](services/ldv-logboek/README.md)
- [Following an LDV chain across logbooks](docs/ldv/README.md)
- [Observability](OBSERVABILITY.md)
- [Security](SECURITY.md)
- [Contributing](CONTRIBUTING.md)
- [Changelog](CHANGELOG.md)

GBO is maintained by the Dutch Ministry of the Interior and Kingdom Relations
(BZK), Digital Government Directorate, with ICTU as technical steward.

## Repository owner

The repository owner is **Jeroen de Kok** (ICTU), reachable at
<jeroen.dekok@ictu.nl>. See `publiccode.yml` for additional repository
metadata.
