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

If you run `make demo-eudi` without `make fsc-ca` first, cert mounts fail. Fix by letting the `demo-*` targets handle it (they depend on `certs`), or run it manually:

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

**Most common**: the `/bri` grant-link is not set in the EDI-Controller UI (`http://localhost:8094`). See `README.md` section *Real FSC end-to-end*, step 3.

**Check**: `curl -X POST http://localhost:9409/inkomensverklaring_2024/ …` (see the same README section). A `UNKNOWN_GRANT_HASH_IN_HEADER` response means the grant-link is missing.

## Developer portal shows empty tabs

Expected with `make demo-minimal` (base only) and `make demo-eudi`: DvTP tabs are empty because `dienstverlener-backend` + `consent-portal-backend` are not running. Use `make demo-full` or `make demo` to bring them up.

## Reset to a clean state

```bash
make demo-down
docker volume rm 05-demo_dev-portal-var fsc-infra_postgres-data 2>/dev/null
```

For a full wipe (including certs):

```bash
make fsc-clean
```
