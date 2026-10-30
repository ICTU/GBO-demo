# eudi-credential-issuer — credential issuer for EUDI wallets (OpenID4VCI 1.0, HAIP 1.0)

Our own credential issuer, meant to replace nl-wallet's `issuance_server` (`services/eudi-issuance-server`) once it covers what the demo needs (#614). It runs next to the current issuance server and is not deployed yet.

It serves two wallet profiles, each under its own credential issuer identifier, so that the metadata of each is consistent on its own:

| Profile | Identifier | For | Rules |
|---|---|---|---|
| `nl` | `<public URL>/nl` | NL Wallet (contract of nl-wallet v0.6.0) | `jwt` key proofs without key attestation; the NL credential preview endpoint; no `tx_code` |
| `eudi` | `<public URL>/eudi` | wallets that follow HAIP and the ARF, such as the EUDI reference wallet | key attestation required (`jwt` proof with `key_attestation`, or the `attestation` proof type) at the configured key storage and user authentication levels (demo: `iso_18045_high`); one credential per attested key |

A Wallet Instance Attestation is accepted only from the wallet providers configured for the profile it is presented to.

## What is real and what is not

| Part | Status |
|---|---|
| Pre-Authorized Code Flow, issuer-initiated, with a Credential Offer by value (OpenID4VCI 1.0 sections 3.5, 4.1) | implemented |
| Credential Issuer Metadata, signed with the access certificate in `x5c` (section 12.2.3; ARF ISSU_32) when `application/jwt` is asked for, unsigned JSON otherwise (section 12.2.2) | implemented; `scope` for every configuration (HAIP section 4.1) |
| OAuth 2.0 Authorization Server Metadata (RFC 8414), path-inserted well-known URLs | implemented |
| Client authentication with a Wallet Instance Attestation and PoP over a server challenge (draft-ietf-oauth-attestation-based-client-auth; HAIP section 4.4) | implemented; the challenge is required |
| DPoP-bound access tokens (RFC 9449) | implemented; server DPoP nonces are not used |
| Nonce endpoint, single-use `c_nonce` (section 7) | implemented |
| Credential endpoint with `credential_identifier`, batch issuance (section 8) | implemented; one request per credential identifier |
| Key attestation in JWT format (Appendix D.1), `attestation` proof type (Appendix F.3), required levels in `key_attestations_required` (ARF ISSU_27d) | implemented for the `eudi` profile |
| SD-JWT VC, `dc+sd-jwt` (HAIP section 6.1) | implemented; every attribute selectively disclosable, `iss` omitted |
| Token Status List, JWT (draft-ietf-oauth-status-list) | implemented; random indices (ARF VCR_17); revocation through the internal API, per entry or per offer reference |
| NL credential preview endpoint | implemented for `nl`; not part of OpenID4VCI; once per access token |
| Authorization Code Flow, PAR | not yet; `response_types_supported` is empty |
| Deferred issuance, notification endpoint, re-issuance with refresh tokens, credential request/response encryption, `tx_code` | not yet |
| ISO mdoc | not yet |
| `credential_reuse_policy` (ETSI TS 119 472-3; ARF ISSU_39) | not yet |
| Wallet provider and key attestation revocation (`client_status`, `status` in the key attestation) | not checked |
| Disclosure-based issuance (presentation before the offer) | not yet; the current issuance server keeps doing this |

**Deviations and accepted risks**

- **The NL preview takes the DPoP-bound token as a bearer token.** RFC 9449 section 7.2 forbids this; the NL Wallet calls the preview without DPoP. The preview answers once per token, so a leaked token discloses the data at most once, and only within the token lifetime.
- **A leaked offer can be redeemed by anyone with a trusted wallet.** The pre-authorized code is the only secret (OpenID4VCI section 13.6.1). A transaction code would help, but the NL Wallet refuses offers with one. Offers therefore live 5 minutes. For `nl`, prefer the universal link on the same device to a QR code.
- **The status list is signed with a separate status certificate, and credentials carry no `iss`.** This follows the NL Wallet and Token Status List section 11.3 (a Status Issuer certificate from the same CA). The NL Wallet also wants that certificate to have the issuer's subject and EKU `1.3.6.1.5.5.7.3.127`, its placeholder for `id-kp-oauthStatusSigning`, whose OID the draft still has as TBD. Wallets that find the status signer through `iss`, such as OID4VCgo, cannot check these status lists. The spec leaves key resolution to the ecosystem.
- **`/challenge` and `/nonce` take unauthenticated requests and store a row each.** Rate limiting belongs at the ingress.

**Demo shortcuts**

- The internal API (offers, revocations) has no authentication. It must only be reachable from the internal network. A real deployment authenticates every caller.
- The bronpoort request carries the subject the offer caller supplied in the shape of a disclosure. The backend cannot tell it from attributes a wallet disclosed. A real deployment needs an authenticated caller and a statement of origin.
- `ISSUER_STORE=memory` keeps all state in memory, for a local run only.

## Endpoints

Public port (`ISSUER_LISTEN_ADDR`, default `:8080`), per profile `p`:

| Endpoint | |
|---|---|
| `GET /.well-known/openid-credential-issuer/{p}` | signed issuer metadata; requires `Accept: application/jwt`, otherwise 406 |
| `GET /.well-known/oauth-authorization-server/{p}` | authorization server metadata |
| `POST /{p}/challenge` | `{"attestation_challenge"}` for the attestation PoP |
| `POST /{p}/token` | pre-authorized code grant; headers `OAuth-Client-Attestation`, `OAuth-Client-Attestation-PoP`, `DPoP` |
| `POST /{p}/nonce` | `{"c_nonce"}` |
| `POST /{p}/credential` | `Authorization: DPoP <token>`, `DPoP`; body with `credential_identifier` and `proofs` |
| `POST /nl/credential_preview` | `Authorization: Bearer <token>`, no DPoP (as the NL Wallet calls it) |
| `GET /status/{list}` | Status List Token (`application/statuslist+jwt`) |
| `GET /healthz` | health |

Internal port (`ISSUER_INTERNAL_ADDR`, default `:8081`):

| Endpoint | |
|---|---|
| `POST /offers` | `{profile, credential_configuration_ids, subject, parameters, reference?}` → `201 {credential_offer, links}`. The issuer fetches the data from the bronpoort now; `404 no_data`, `503 source_unavailable` or `502 bad_source_data` when that fails, and nothing is stored. `reference` is the caller's own reference for revocation |
| `POST /revocations` | `{reference}` revokes every credential issued for offers with that reference → `200 {revoked}`; `{list, index}` revokes one entry → 204. Anything else is a 400: a revocation cannot be undone, so nothing is guessed |
| `GET /healthz` | health |

## Bronpoort

For each credential configuration the issuer posts to `source_url` (query: the offer parameters) the body the attestation server of nl-wallet's `issuance_server` receives, with the subject as the attributes of one attestation:

```json
[{"id": "subject", "attestations": [{"attestation_type": "subject", "attributes": {"bsn": "999990019"}}]}]
```

It expects `[{"id", "format": "dc+sd-jwt", "attestation_type": "<vct>", "attributes": {...}}]`. `eudi-adapter` and `qtsp-reference` implement this contract already. The offer is refused with `bad_source_data` when what the source returns cannot be issued:

- an attribute is not described by a claim of the credential configuration, or a mandatory claim is missing (the NL Wallet refuses undescribed attributes);
- a top-level attribute uses a name SD-JWT VC reserves (`iss`, `vct`, `exp`, `status`, ...);
- for `nl`: a number is not a whole number (the NL Wallet knows no others).

## Run

```bash
cd services/eudi-credential-issuer
go test ./...
# PostgreSQL store tests:
ISSUER_TEST_DATABASE_URL=postgres://... go test ./internal/postgres/
```

| Environment | |
|---|---|
| `ISSUER_PUBLIC_URL` | external base URL, without trailing slash; issuer identifiers are `<public URL>/<profile>` |
| `ISSUER_CONFIG_FILE` | JSON configuration (below) |
| `ISSUER_DATABASE_URL`, `ISSUER_DATABASE_SCHEMA` | PostgreSQL; schema default `eudi_credential_issuer` |
| `ISSUER_STORE=memory` | demo shortcut: in-memory state |
| `ISSUER_LISTEN_ADDR`, `ISSUER_INTERNAL_ADDR` | ports |

Interoperability with an independent wallet: `interop/` is a separate module that drives the issuer with OID4VCgo (OpenID Certified wallet). It needs Go 1.26.10 (`GOTOOLCHAIN=auto` fetches it) and does not run in CI.

```bash
cd services/eudi-credential-issuer/interop && go test ./...
```

`eudi-credential-issuer migrate` applies the migrations; `serve` (the default) does not. Expired state is purged every minute; sessions, which hold the fetched attributes, are deleted once no access token can use them.

Configuration file (paths relative to the file):

```json
{
  "issuer": {"key_file": "issuer-key.pem", "chain_file": "issuer-chain.pem"},
  "status": {"key_file": "status-key.pem", "chain_file": "status-chain.pem"},
  "batch_size": 4, "offer_ttl": "5m", "token_ttl": "10m", "nonce_ttl": "5m", "status_list_size": 65536,
  "profiles": [
    {"name": "nl", "kind": "nl", "wallet_provider_roots_file": "nl-wallet-providers.pem",
     "access": {"key_file": "access-key.pem", "chain_file": "access-chain.pem"},
     "registration_certificate_file": "wrprc.jwt",
     "universal_link_base": "https://app.preproductie.wallet.edi.bzk.nl/deeplink"},
    {"name": "eudi", "kind": "eudi", "wallet_provider_roots_file": "eudi-wallet-providers.pem",
     "key_attestation_roots_file": "eudi-wallet-providers.pem",
     "key_storage": ["iso_18045_high"], "user_authentication": ["iso_18045_high"],
     "access": {"key_file": "access-key.pem", "chain_file": "access-chain.pem"}}
  ],
  "credential_configurations": [
    {"id": "inkomensverklaring", "scope": "inkomensverklaring", "vct": "https://.../types/belastingdienst/inkomensverklaring/v2.4",
     "valid_days": 365, "source_url": "https://eudi-adapter/attestations/belastingdienst/inkomensverklaring",
     "status_list": "inkomensverklaring",
     "display": [{"name": "Inkomensverklaring", "locale": "nl"}],
     "claims": [{"path": ["belastingjaar"], "mandatory": true}]}
  ]
}
```

The issuer refuses to start with a configuration a wallet would refuse: no `scope`, `display` or claims, an empty claim path, an `nl` profile without registration certificate, an `eudi` profile without key levels, duplicate names, a public URL with a path, or a chain file with a self-signed certificate (HAIP: no trust anchor in `x5c`). The status certificate must have the subject of the issuer certificate. The NL Wallet also expects the issuer certificate to carry EKU `1.0.18013.5.1.2` and the status certificate EKU `1.3.6.1.5.5.7.3.127`; the issuer does not check its own certificates.

## Layout

| Package | |
|---|---|
| `main.go` | composition root: environment, store, servers, purge |
| `internal/issuance` | core: offers, metadata, token, nonce, credential, preview, status lists, configuration checks; ports for source and stores |
| `internal/httpapi` | HTTP adapter, public and internal; the end-to-end tests of all cases |
| `internal/source` | bronpoort client |
| `internal/postgres`, `internal/memstore` | stores; `internal/storetest` is the contract both pass |
| `internal/config` | configuration file |
| `interop/` | separate module: the issuer driven by the OID4VCgo wallet |
| `../oid4vc-kit` | protocol building blocks shared with future services |

## Interpretations and choices

- Credential `nbf` is the start of the day (UTC) the offer was made, `exp` is `nbf` plus `valid_days`. Both are fixed when the offer is made, so the NL preview equals the issued credential. `iat` is the start of the day of issuance, so the copies of a batch cannot be linked by it (RFC 9901 section 10.1).
- All proofs of one request must carry the same `c_nonce`, which is used up by the request. A proof without `c_nonce` is `invalid_proof`; an unknown or used one is `invalid_nonce`.
- For `nl`, two proofs with the same key are refused: one credential per proof.
- A credential is marked as issued only after it is signed, so a failed request leaves it issuable.
- A `client_id` in the token request must equal the `sub` of the Wallet Instance Attestation.
- The signing certificate of an incoming Wallet Instance Attestation or key attestation must not be self-signed (HAIP).
- For the `eudi` profile every key in a key attestation gets a credential (Appendix F.1: "SHOULD issue a Credential for each cryptographic public key specified in the attested_keys"); the number of keys is bounded by `batch_size`.
- Client authentication and DPoP are checked before the pre-authorized code is used, and a code is only redeemed on the profile of its offer, so a refused request does not burn the code. The attestation challenge is used up either way.
- DPoP `htu` is compared with the public URL, not the request URL, because the issuer runs behind a proxy.
- Server-provided DPoP nonces are not used: the NL Wallet does not retry on `use_dpop_nonce`. Replay of a DPoP proof is blocked by its `jti` within the 60-second `iat` window.
- The `iss` claim of a key proof is not checked against the client.
