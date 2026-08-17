# Troubleshooting

Common problems on first boot of the demo stack. See also `README.md`.

## Port conflicts

The demo uses ports `9000-9686`. If something fails with "port already allocated":

```bash
lsof -i -P -n | grep LISTEN | grep -E ":9[0-9]{3}"
```

Fix by running `make demo-down` (or `docker compose down` for the relevant compose file), or stop the conflicting service.

## Docker resource limits

`make demo-eudi` and `make demo-full` start ~25 containers. Minimum:

- **RAM**: 8 GB allocated to Docker (Preferences → Resources → Memory)
- **CPU**: no strict requirement, but slow below 4 cores
- **Disk**: ~10 GB for images

Symptoms of insufficient RAM: containers crash at random, `docker compose logs` shows OOM kills.

## Missing certificates

The `demo-*` targets generate the local FSC certificates in the correct order.
Certificate mount failures normally mean the stack was started directly with
`docker compose` before bootstrap completed. Prefer `make demo-eudi`, or run
the certificate targets manually:

```bash
make fsc-ca && make fsc-directory-certs && make fsc-edi-certs && make fsc-bd-certs
```

## Migrations racing

On the first `make demo-eudi`, postgres containers and migration jobs start together. If a migration opens the DB before postgres is healthy, retry:

```bash
docker compose -f fsc-infra/docker-compose.yml logs edi-migrations-manager
```

Restart the migration container with `docker compose … restart edi-migrations-manager`.

## Contract seed fails

Check the `make fsc-seed-bri` output. Common causes:

- **HTTP 500 `unknown protocol`**: manager v2.4.0 versus script mismatch. Verify that `services/dev-portal-backend/fsctxlog.go` and this script use the same FSC version.
- **HTTP 401**: manager is missing the auto-sign flag. Check `fsc-infra/docker-compose.yml` — `directory-manager` needs `--auto-sign-grants=servicePublication` and `bd-manager` needs `--auto-sign-grants=serviceConnection`.
- **Contract stays pending**: auto-sign polls asynchronously; wait 5-10s and check state:
  ```bash
  docker run --rm --network fsc-infra_default \
    -v $(pwd)/fsc-infra:/work:ro gbo-demo/pki-tools:local \
    bash -c 'curl -s --cert /work/orgs/belastingdienst-mock/pki/internal/internal-cert.pem \
                    --key /work/orgs/belastingdienst-mock/pki/internal/internal-cert-key.pem \
                    --cacert /work/orgs/belastingdienst-mock/pki/internal/intermediate_ca.pem \
                    "https://bd-manager:9443/v1/contracts?grant_type=GRANT_TYPE_SERVICE_PUBLICATION" | jq'
  ```

## EUDI flow doesn't work after `make demo-eudi`

Start with the generated and activated state:

```bash
find .local/onboarding/active -type f -name '*.json' -print
jq -r '.[].key' services/eudi-issuance-server/config/eudi-offers.json
docker compose ps
```

`make demo-eudi` should create three active source records and four offers:
`inkomensverklaring_2024`, `inkomensverklaring_2025`,
`akte_van_overlijden`, and `demo_inkomen_2025`. The last offer is the
explicitly unsecured HTTP demonstration.

**The QR opens but no applicable PID can be shared**: `make demo-eudi` does not
onboard the wallet or issue a PID. Use a v0.5.0-compatible test wallet with a
trusted `urn:eudi:pid:nl:1` credential. Mock BSN `999991772` is the persona that
has data for all three offers. Also verify that the wallet trusts the issuer
and reader CA certificates installed under `.local/secrets/development-ca`.

**`source metadata unavailable`**: reconciliation did not produce a usable
activation. Rerun the complete idempotent sequence and inspect the reconciler
output:

```bash
make onboard-demo-sources
make eudi-config
docker compose --profile eudi up --build --force-recreate -d \
  eudi-adapter eudi-issuance-server developer-portal landing-page
```

**The wallet fails after PID sharing and the issuance-server logs `requested
credential previews not found in session`**: compare the public and local
issuer metadata. With Cloudflare, `CF-Cache-Status: HIT` or an `Age` header on
the issuance hostname means a broad cache rule is overriding the origin's
`Cache-Control: no-store`. Add a final hostname-wide **Bypass cache** rule,
purge the hostname, reload the QR page, and scan a newly generated QR.

```bash
set -a; . ./.env; set +a
curl -sSI "${EUDI_PUBLIC_URL%/}/.well-known/openid-credential-issuer" \
  | grep -Ei 'cf-cache-status|age|cache-control'
curl -s "${EUDI_PUBLIC_URL%/}/.well-known/openid-credential-issuer" \
  | jq -r '.credential_configurations_supported | keys[]'
```

Expect `DYNAMIC` or `BYPASS`, no increasing `Age`, and the same VCT version as
the active source record.

**FSC seed reports PostgreSQL password authentication failed**: an existing
FSC volume was initialized with a different password than `fsc-infra/.env`.
For disposable local state, run `make fsc-clean` and restart. Do not wipe a
non-disposable environment.

**Check**: `curl -X POST 'http://localhost:9409/attestations/99999999900000000200/inkomensverklaring?jaar=2024' …`. A `UNKNOWN_GRANT_HASH_IN_HEADER` response means the data-service grant-link is missing.

## Developer portal shows empty tabs

Expected with `make demo-minimal` (base only) and `make demo-eudi`: DvTP tabs are empty because `dienstverlener-backend` + `consent-portal-backend` are not running. Use `make demo-full` or `make demo` to bring them up.

## Reset to a clean state

```bash
make demo-down
make clean
```

For a full FSC wipe (including contracts and locally generated FSC
certificates):

```bash
make fsc-clean
```
