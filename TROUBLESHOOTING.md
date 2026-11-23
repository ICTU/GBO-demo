# Troubleshooting

The Aspire dashboard (<http://localhost:15126>) is the first stop: it shows
every resource's state and health, the exit code of every run-once step
(certificates, migrations, seeds) and each resource's console log. From a
terminal in `aspire/`, `aspire describe` lists the same states and
`aspire logs <resource>` prints one resource's console.

## A resource waits forever or a run-once step failed

The AppHost starts resources in dependency order: a resource waits until the
ones it depends on are healthy, or for run-once steps, have exited 0. A step
that fails holds up everything after it. Find the first resource that is not
`Running`/`Healthy` or exited non-zero in the dashboard and read its console
log. Run-once steps leave the list once they succeed; a running or failed one
stays, and "Show hidden resources" in the dashboard (or
`aspire describe --include-hidden`) shows the finished ones. Restart a
run-once step from its row in the dashboard once the cause is fixed; the FSC
certificate steps and the seeds are idempotent.

## Port conflicts

The AppHost publishes fixed ports where a URL or certificate depends on them:

- the frontends: 9000–9004;
- the FSC Controllers and certportal: 8091–8102, and the EDI Outway on 8087;
- the EUDI and ASI services: 9409, 9424–9432 and 18001;
- the dashboard: 15126, 19025 and 20111.

Every other port is assigned per run and listed in the dashboard. If a
resource fails with "address already in use", find the other listener:

```bash
lsof -nP -iTCP -sTCP:LISTEN | grep -E ':(9[0-9]{3}|8[01][0-9]{2}|15126|18001)\b'
```

## "Blocked request" or 403 from another machine

Vite's dev server refuses host names it does not know. Start the AppHost with
the host name the browser uses, plus any other names, comma-separated:

```bash
aspire run -- --Gbo:PublicHost=demo.example.org --Gbo:ExtraAllowedHosts=alias.example.org
```

The same setting fixes links that point at `localhost`.

## `npm ci` fails in a frontend installer

The `*-installer` resources run `npm ci`, which needs npm 11 (Node.js 24 or
newer, as in CI). npm 10 reads the lockfiles as out of sync (`Missing:
@types/react@… from lock file`). The installers run the first `npm` on the
PATH that `aspire run` was started with, so the AppHost checks that one and
stops with a message when it is too old, naming a newer Node.js it found
(Homebrew, nvm, Volta, fnm). Put that directory first on PATH, or install one
(`brew install node`).

## Docker resource limits

The `dvtp` profile runs about 60 long-running containers, most of them FSC
infrastructure, plus run-once steps; `full` runs more. The containers need
about 1 GB of RAM for `dvtp`; give Docker at least 8 GB for `full`, and at
least 4 cores.

Symptoms of insufficient RAM: containers crash at random, and the dashboard
shows them exiting with code 137 (OOM kills).

## Missing certificates

The AppHost creates the local FSC certificates before it starts the FSC
peers: `fsc-root-ca`, then one `<peer>-certs` step per peer, through
`aspire/scripts/fsc-pki.sh`. Certificate mount failures mean one of those
steps failed; its console log in the dashboard names the cause. The steps
skip an organisation whose certificates exist, so after a fix restart the
failed step.

## Migrations racing

Each FSC migration step waits for its database (`<peer>-db-<part>`, nested
under `fsc-postgres`), which Aspire's PostgreSQL integration creates once the
server is healthy. A migration that still fails shows its error in its console
log; restart it from the dashboard.

## Contract seed fails

Check the console log of the failing `fsc-seed-*` step. Each seed retries a
few times, since the FSC Managers have no health check. Common causes:

- **HTTP 500 `password authentication failed for user "postgres"`**: the FSC
  database volume (`gbo-fsc-postgres-data`) was initialized with a different
  password than `fsc-postgres-password` in the AppHost secrets. This happens
  when the secrets are reset but the volume is kept. When the FSC state is
  disposable, run `aspire/scripts/fsc-clean.sh` and start again.
- **HTTP 500 `unknown protocol`**: manager v2.4.0 versus script mismatch. Verify that `services/dev-portal-backend/fsctxlog.go` and this script use the same FSC version.
- **HTTP 401**: inspect the responding Manager's logs and the client
  certificate used by the seed. Auto-sign configuration is separate:
  `directory-manager` uses `--auto-sign-grants=servicePublication`; source
  Managers use `--authzen-auto-sign-pdp-address` for connection contracts.
  These two options cannot be combined on one Manager.
- **Publication contract stays pending**: `directory-manager` signs these by
  grant type. Wait 5-10s and check the publication state at `bd-manager`,
  from a container on the AppHost's network:
  ```bash
  net=$(docker inspect -f '{{range $n, $_ := .NetworkSettings.Networks}}{{$n}}{{end}}' \
    "$(docker ps -q --filter label=com.docker.compose.service=bd-manager | head -1)")
  docker run --rm --network "$net" \
    -v "$(pwd)/fsc-infra:/work:ro" gbo-demo/pki-tools:local \
    bash -c 'curl -s --cert /work/orgs/belastingdienst-mock/pki/internal/internal-cert.pem \
                    --key /work/orgs/belastingdienst-mock/pki/internal/internal-cert-key.pem \
                    --cacert /work/orgs/belastingdienst-mock/pki/internal/intermediate_ca.pem \
                    "https://bd-manager:9443/v1/contracts?grant_type=GRANT_TYPE_SERVICE_PUBLICATION" | jq'
  ```

- **Connection contract stays pending**: the source Manager asks the
  auto-sign PDP to decide whether to counter-sign. Inspect the decision in
  the `openftv-pdp` console log (dashboard, or `aspire logs openftv-pdp` in
  `aspire/`):

  ```bash
  aspire logs openftv-pdp | grep auto_sign_contract | tail -1 | sed 's/^[^{]*//' | jq .result.response
  ```

  `ADMISSION_REGISTER_UNAVAILABLE` means the PDP has no
  `data.entities.dvtp_participant` document: check the onboarding register
  and OpenFTV PIP pull. `PARTY_NOT_IN_REGISTRY` means the document is present
  but does not contain the peer: admit that peer in the register. The source
  Manager does not counter-sign in either case.

## Every request denies with `NO_APPLICABLE_RULE`

`NO_APPLICABLE_RULE` is the engine's closed-world default: no rule declares
the requested field. Check the field against `covers_fields` in
`policies/gbo/rules/` first — that, not the grant, is the catalog. A rule
that names `services` binds only on those FSC services: `ASI0001` covers
`Query.ingeschrevenPersoon` on `brp`, not on `bri`.

The authorization regime itself no longer travels in the token. It follows
from the evidence on the request: a consent token selects the consent
regime, and its absence the PID regime — the two exclude each other by
construction. So a deny that names a consent or PID axis is about the
request, not about a missing grant property.

The bron-sidecar follows the same evidence: with a consent token it puts the
citizen of that consent in the place of the placeholder, and without one it
passes the request through. It reads no grant property, and the seeds set
none. Decode the token to see what the provider Manager actually issued:

```bash
aspire logs openftv-pdp | grep -o 'Bearer [A-Za-z0-9._-]*' | tail -1 \
  | cut -d' ' -f2 | cut -d. -f2 \
  | python3 -c 'import base64,json,sys; d=sys.stdin.read().strip(); print(json.dumps(json.loads(base64.urlsafe_b64decode(d+"="*(-len(d)%4))), indent=2))'
```

An empty or missing `prp` means the connection contract carries no grant
properties, which is what the seeds create. A `prp` with `subject_id_type`
comes from a contract seeded before that property was dropped. It does no
harm, since nothing reads it. Re-run the seed to clear it: it detects the
property mismatch, creates a new contract (contracts are immutable) and
repoints the grant-link.

Each seed step repairs only its own grants. Restart them from the dashboard
in this order, since `fsc-seed-bri-hv` needs the `bri` publication the first
creates: `fsc-seed-bri`, `fsc-seed-rvig-source`, `fsc-seed-metadata-bd`,
`fsc-seed-metadata-rvig`, `fsc-seed-bri-hv`, `fsc-seed-consent-status`.

The `svc` claim in the decoded token names the failing service. To repair just
that one:

| `svc` | Consumer → provider | Seed step |
| --- | --- | --- |
| `bri` | EDI-issuer → Belastingdienst | `fsc-seed-bri` |
| `bri` | Hypotheekverlener → Belastingdienst | `fsc-seed-bri-hv` |
| `brp` | EDI-issuer → Belastingdienst (RvIG source) | `fsc-seed-rvig-source` |
| `gbo-metadata-bd`, `gbo-metadata-rvig` | EDI-issuer → Belastingdienst | `fsc-seed-metadata-bd`, `fsc-seed-metadata-rvig` |
| `consent-status` | PDP → Toestemmingsregister | `fsc-seed-consent-status` |

The `sub` claim tells the two `bri` rows apart: `99999999900000000100` is the
EDI-issuer, `99999999900000000300` the Hypotheekverlener.

If the new contract stays pending, the counterparty refused it; see *Contract
seed fails* above. Grant properties need OpenFSC >= v2.0.0 on every peer on
the contract.

## EUDI flow doesn't work under the `eudi` or `full` profile

If the dashboard shows `eudi-not-configured` (exit code 1), the EUDI parts
were left out: its log names the missing development CAs or wallet
registration. Add them and start the AppHost again.

Start with the activated state and the offers:

```bash
curl -s http://localhost:9409/eudi-offers.json | jq -r '.[].key'
```

and check in the dashboard that `source-reconciler` and
`eudi-issuance-materialize` exited 0. A working onboarding has three active
source records and four offers: `inkomensverklaring_2024`,
`inkomensverklaring_2025`, `akte_van_overlijden`, and `demo_inkomen_2025`.
The last offer is the explicitly unsecured HTTP demonstration.

**The QR opens but no applicable PID can be shared**: the demo does not
onboard the wallet or issue a PID. Use a v0.5.0-compatible test wallet with a
trusted `urn:eudi:pid:nl:1` credential. Mock BSN `999991772` is the persona that
has data for all three offers. Also verify that the wallet trusts the issuer
and reader CA certificates installed under `.local/secrets/development-ca`.

**`aspire/scripts/provision.sh publish-trust-lists` fails with `requires its own GBO root`**: the
issuer or reader CA files are not the GBO CAs (CN `ca.gbo-issuer` and
`ca.gbo-reader`). Put the four CA files under `.local/secrets/development-ca/`
(or the directory `Gbo:SecretsDir` points at), then run the provisioning again. Published
wallet builds trust only the managed CAs; a CA of your own works only with a
wallet you build yourself.

**`source metadata unavailable`**: reconciliation did not produce a usable
activation. Read the `source-reconciler` console log, then restart in the
dashboard, in this order: `source-reconciler`, `eudi-issuance-materialize`,
`eudi-issuance-server`, `eudi-adapter`.

**The wallet fails after PID sharing and the issuance-server logs `requested
credential previews not found in session`**: compare the public and local
issuer metadata. With Cloudflare, `CF-Cache-Status: HIT` or an `Age` header on
the issuance hostname means a broad cache rule is overriding the origin's
`Cache-Control: no-store`. Add a final hostname-wide **Bypass cache** rule,
purge the hostname, reload the QR page, and scan a newly generated QR.

```bash
EUDI_PUBLIC_URL=$(cd aspire && aspire secret get Parameters:eudi-public-url --non-interactive | tail -1)
curl -sSI "${EUDI_PUBLIC_URL%/}/.well-known/openid-credential-issuer" \
  | grep -Ei 'cf-cache-status|age|cache-control'
curl -s "${EUDI_PUBLIC_URL%/}/.well-known/openid-credential-issuer" \
  | jq -r '.credential_configurations_supported | keys[]'
```

Expect `DYNAMIC` or `BYPASS`, no increasing `Age`, and the same VCT version as
the active source record.

**FSC seed reports PostgreSQL password authentication failed**: see
[Contract seed fails](#contract-seed-fails). Do not wipe a non-disposable
environment.

**Check**: `curl -X POST 'http://localhost:9409/attestations/99999999900000000200/inkomensverklaring?jaar=2024' …`. A `UNKNOWN_GRANT_HASH_IN_HEADER` response means the data-service grant-link is missing.

## Developer portal shows empty tabs

Expected without the `dvtp` profile: the DvTP tabs are empty because
`dienstverlener-backend` and `consent-portal-backend` are not running. Start
with the default (dvtp) or `Gbo__Profiles=full`.

## Traces or decision details are missing in the developer portal

Locally the portal reads traces and the PDP's decision log from the Aspire
dashboard. Check that the `otel-collector` resource is healthy and that the
dashboard's Traces page shows the request. The dashboard keeps one store
across restarts (Resume persistence); spans older than its limits
(100,000 traces) are evicted.

## Reset to a clean state

Stop the AppHost with Ctrl+C, then remove the volumes it created (all named
`gbo-*`):

```bash
docker volume ls -q --filter name='^gbo-' | xargs docker volume rm
```

For a full FSC wipe (including contracts and locally generated FSC
certificates):

```bash
aspire/scripts/fsc-clean.sh
```
