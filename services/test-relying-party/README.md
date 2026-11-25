# test-relying-party — test relying party of the developer portal

A demo tool (#737): two fictional relying parties ask a wallet for an
attestation of the own credential issuer over OpenID4VP 1.0 (HAIP 1.0), on
the attestation type and claims chosen in the developer portal. For every
answer it records which copy was presented to whom: the fingerprint of the
holder key in `cnf`, the same fingerprint the issuer's timeline shows per
issued copy. The portal puts a presented copy next to the issuance it came
from.

It is not a relying party of the GBO chain and not part of the issuer.

## What it records

| Event | When | Holds |
|---|---|---|
| `presented` | the presentation verified | relying party, attestation type, the claim names asked, key fingerprint, status (`valid`, `revoked`, `expired`) |
| `declined` | the wallet answered with an error, for example `access_denied` | relying party, attestation type, claim names, the error |
| `rejected` | the presentation did not verify (signature, key binding, nonce, claims other than asked) | relying party, attestation type, claim names, the reason |

A revoked or expired copy is recorded as presented, with its status: the
point is to see what a wallet presents. The verifier of `oid4vc-kit` does
this with `ReportStatus`; an unreachable status list is still a rejection.
No event holds an attribute value, a token, a nonce or a full key: the
disclosed values are dropped after verification. Events are kept in memory
for 50 minutes.

## Endpoints

Public port (`RP_LISTEN_ADDR`, default `:8080`), reached by the wallet:

| Endpoint | |
|---|---|
| `GET /request/{id}` | the signed request object (`application/oauth-authz-req+jwt`): `client_id` `x509_hash:` of the relying party's WRPAC, its WRPRC in `verifier_info`, a DCQL query for one SD-JWT VC of the type with the chosen claims, `direct_post.jwt` with an ephemeral P-256 key. `404` for an unknown or expired request |
| `POST /response/{id}` | the wallet's answer (`response`, or `error`): `200 {}`, or `400 invalid_request` for a rejected presentation or an unknown, answered or expired request |
| `GET /healthz` | health |

Internal port (`RP_INTERNAL_ADDR`, default `:8081`), for the portal backend
only, without authentication:

| Endpoint | |
|---|---|
| `GET /catalog` | the relying parties and the attestation types and claims they are registered for |
| `POST /presentations` | `{relying_party, credential, claims: [[path...]]}` → `201 {link, request_uri, session, expires_at}`; the `openid4vp://` link is valid 5 minutes. `404 unknown_relying_party`, `404 unknown_credential`, `400 invalid_claims` (claims not registered for the type, or twice) |
| `GET /events` | `{events: [...]}`, the last 50 minutes, oldest first |

## Configuration

`RP_CONFIG_FILE` names the configuration that `make
provision-test-relying-parties` writes. That command registers the two
parties (WRPAC under the development reader CA, WRPRC with entitlement
Service_Provider) for every attestation type of the issuer's `eudi`
profile with all its claims, read from the running issuer's metadata, and
copies the issuer CAs the service trusts. The wallet trusts the parties
through the CA anchors on the WRPAC and WRPRC trust lists; no wallet build
is needed. Run it again after a new attestation type.

| Variable | |
|---|---|
| `RP_CONFIG_FILE` | the configuration |
| `RP_LISTEN_ADDR`, `RP_INTERNAL_ADDR` | ports |
| `RP_STATUS_URL_MAP` | `public=internal` pairs: status lists under the issuer's public URL are read from the issuer itself |

## Run

```bash
make credential-issuer-up
make provision-test-relying-parties   # TEST_RP_PUBLIC_URL: the public https URL the wallet reaches
make test-relying-party-up
```

Set `TEST_RP_INTERNAL_URL=http://test-relying-party:8081` in `.env` for
the portal's form, and route the `TEST_RP_PUBLIC_URL` host to
`test-relying-party:8080` in the tunnel. `go test ./...` runs the cases of
#737 with a test wallet.
