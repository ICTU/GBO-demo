# eudi-credential-issuer — credential issuer for EUDI wallets (OpenID4VCI 1.0, HAIP 1.0)

Our own credential issuer, meant to replace nl-wallet's `issuance_server` (`services/eudi-issuance-server`) once it covers what the demo needs (#614). It runs next to the current issuance server and is not deployed yet.

It hosts several **providers**: each source (bronhouder) is its own Attestation Provider, with its own access certificate (WRPAC), registration certificate (WRPRC), signing and status keys, credential configurations and status lists. A wallet sees the provider, not the issuer service.

Each provider is published under two wallet **profiles** (three with a combined one), each its own credential issuer identifier `<public URL>/<provider>/<profile>`, so that the metadata of each is consistent on its own:

| Profile | Identifier (provider `belastingdienst`) | For | Rules |
|---|---|---|---|
| `nl` | `<public URL>/belastingdienst/nl` | NL Wallet (contract of nl-wallet v0.6.0) | `jwt` key proofs without key attestation; the NL credential preview endpoint; no `tx_code` |
| `eudi` | `<public URL>/belastingdienst/eudi` | wallets that follow HAIP and the ARF, such as the EUDI reference wallet | key attestation required (`jwt` proof with `key_attestation`, or the `attestation` proof type) at the configured key storage and user authentication levels (demo: `iso_18045_high`); one credential per attested key; encrypted credential requests and responses supported, not required |
| kind `combined` (optional, e.g. `wallet`) | `<public URL>/belastingdienst/wallet` | both, so one offer (one QR code, `openid-credential-offer://`) reaches either wallet | the metadata offers the `jwt` proof type without key attestation and the `attestation` proof type with it; a wallet whose WIA comes from a provider in `plain_jwt_wallet_providers_file` (the NL Wallet's) may send plain `jwt` proofs, any other wallet must attest its keys as on `eudi`. The access token remembers the wallet provider. Encryption and the NL preview endpoint are offered too. No disclosure offers. **NL Wallet v0.6.0 cannot read it yet**: it expects `credential_request_encryption.jwks` as a bare array of keys instead of a JWK Set (`{"keys": [...]}`, which OpenID4VCI and the EUDI wallets use, and which the EUDI wallet for Android needs). nl-wallet fixed this after v0.6.0 (`df80f99c3`); until a release carries it, the NL Wallet uses the `nl` profile or `eudi-issuance-server` |

A Wallet Instance Attestation is accepted only from the wallet providers configured for the profile it is presented to. The combined profile is a documented exception to its own metadata: the NL Wallet cannot attest keys yet (nl-wallet v0.6.0, PVW-6238), so its wallets skip the key attestation that EUDI wallets must give. A pre-authorized code or access token works only at the provider and profile of its offer, and a `c_nonce` or attestation challenge only at the provider and profile that handed it out.

## What is real and what is not

| Part | Status |
|---|---|
| Pre-Authorized Code Flow, issuer-initiated, with a Credential Offer by value (OpenID4VCI 1.0 sections 3.5, 4.1) | implemented |
| Credential Issuer Metadata, signed with the access certificate in `x5c` (section 12.2.3; ARF ISSU_32) when `application/jwt` is asked for, unsigned JSON otherwise (section 12.2.2) | implemented; `scope` for every configuration (HAIP section 4.1); `display` names the issuer by the organization name (O) of the access certificate, without a locale, as wallets show it (section 12.2.4); an access certificate without one is refused at start |
| OAuth 2.0 Authorization Server Metadata (RFC 8414), path-inserted well-known URLs | implemented |
| Client authentication with a Wallet Instance Attestation and PoP over a server challenge (draft-ietf-oauth-attestation-based-client-auth -07, as OpenID4VCI 1.0 references it; HAIP section 4.4) | implemented; the challenge is required, and a missing or unknown one is answered with `use_attestation_challenge` (400) and a fresh challenge in `OAuth-Client-Attestation-Challenge`. `iss` and `iat` are not required in the attestation, so attestations made after -07 and after -12 are both accepted |
| DPoP-bound access tokens (RFC 9449) | implemented; server DPoP nonces are not used |
| Nonce endpoint, single-use `c_nonce` (section 7) | implemented |
| Credential endpoint with `credential_identifier`, batch issuance (section 8) | implemented; one request per credential identifier |
| Encrypted credential requests and responses (sections 8.2 and 10) | implemented for the `eudi` profile, not required: ECDH-ES on P-256 with A128GCM or A256GCM, no compression. A wallet that asks for an encrypted response must encrypt its request (section 8.2), otherwise `invalid_encryption_parameters`. Each provider has its own request key; its `kid` is the JWK thumbprint, so a wallet holding metadata from before a new key gets `invalid_credential_request` until it fetches the metadata again. Errors are not encrypted. On `nl`, an encrypted request or a `credential_response_encryption` parameter is an `invalid_credential_request` |
| Key attestation in JWT format (Appendix D.1), `attestation` proof type (Appendix F.3), required levels in `key_attestations_required` (ARF ISSU_27d) | implemented for the `eudi` profile |
| SD-JWT VC, `dc+sd-jwt` (HAIP section 6.1) | implemented; every attribute selectively disclosable, `iss` omitted |
| Token Status List, JWT (draft-ietf-oauth-status-list) | implemented; random indices (ARF VCR_17); revocation through the internal API, per entry or per offer reference |
| NL credential preview endpoint | implemented for `nl`; not part of OpenID4VCI; once per access token |
| Authorization Code Flow, PAR | not yet; `response_types_supported` is empty |
| Deferred issuance, notification endpoint, re-issuance with refresh tokens, `tx_code` | not yet |
| ISO mdoc | not yet |
| `credential_reuse_policy` (ETSI TS 119 472-3; ARF ISSU_39) | not yet |
| Wallet provider and key attestation revocation (`client_status`, `status` in the key attestation) | not checked |
| Disclosure-based issuance (presentation before the offer) | not yet; the current issuance server keeps doing this |

**Deviations and accepted risks**

- **The NL preview takes the DPoP-bound token as a bearer token.** RFC 9449 section 7.2 forbids this; the NL Wallet calls the preview without DPoP. The preview answers once per token, so a leaked token discloses the data at most once, and only within the token lifetime.
- **A leaked offer can be redeemed by anyone with a trusted wallet.** The pre-authorized code is the only secret (OpenID4VCI section 13.6.1). A transaction code would help, but the NL Wallet refuses offers with one. Offers therefore live 5 minutes. For `nl`, prefer the universal link on the same device to a QR code.
- **The status list is signed with a separate status certificate, and credentials carry no `iss`.** This follows the NL Wallet and Token Status List section 11.3 (a Status Issuer certificate from the same CA). The NL Wallet also wants that certificate to have the issuer's subject and EKU `1.3.6.1.5.5.7.3.127`, its placeholder for `id-kp-oauthStatusSigning`, whose OID the draft still has as TBD. Wallets that find the status signer through `iss`, such as OID4VCgo, cannot check these status lists. The spec leaves key resolution to the ecosystem.
- **`/challenge` and `/nonce` take unauthenticated requests and store a row each.** Rate limiting belongs at the ingress.
- **Each provider has its own status lists.** A list holds only the credentials of one provider and configuration, so with few credentials the crowd that hides a lookup (Token Status List section 12) is small. The issuer pads nothing; a list is as large as configured.

**Demo shortcuts**

- The internal API (offers, revocations) has no authentication. It must only be reachable from the internal network. A real deployment authenticates every caller.
- The bronpoort trusts the subject the offer caller names; the internal API does not authenticate that caller. A real deployment authenticates it, so the backend knows who vouches for the subject.
- Revocation by reference spans providers: a reference is the caller's, not a provider's, so `{reference}` revokes what every provider issued for it. A real deployment lets each provider revoke only its own credentials.
- `ISSUER_STORE=memory` keeps all state in memory, for a local run only.

## Endpoints

Public port (`ISSUER_LISTEN_ADDR`, default `:8080`), per provider `v` and profile `p`:

| Endpoint | |
|---|---|
| `GET /.well-known/openid-credential-issuer/{v}/{p}` | issuer metadata with the provider's own configurations: signed with its access certificate (`application/jwt`) when asked for, unsigned JSON otherwise (`Vary: Accept`) |
| `GET /.well-known/oauth-authorization-server/{v}/{p}` | authorization server metadata |
| `POST /{v}/{p}/challenge` | `{"attestation_challenge"}` for the attestation PoP |
| `POST /{v}/{p}/token` | pre-authorized code grant; headers `OAuth-Client-Attestation`, `OAuth-Client-Attestation-PoP`, `DPoP` |
| `POST /{v}/{p}/nonce` | `{"c_nonce"}` |
| `POST /{v}/{p}/credential` | `Authorization: DPoP <token>`, `DPoP`; body with `credential_identifier` and `proofs`, as JSON or, on `eudi`, as a JWE (`application/jwt`); the response is a JWE when the request carries `credential_response_encryption` |
| `POST /{v}/nl/credential_preview` | `Authorization: Bearer <token>`, no DPoP (as the NL Wallet calls it) |
| `GET /status/{v}/{list}` | the provider's Status List Token (`application/statuslist+jwt`), signed with its status certificate |
| `GET /{v}/{p}/vp/{offer}/request` | disclosure-based issuance: the `request_uri` of a disclosure offer. Each fetch starts a disclosure session (`disclosure_ttl`, default 5 minutes) and returns its OpenID4VP request object (`application/oauth-authz-req+jwt`), signed with the provider's access certificate, asking for the `bsn` of the PID |
| `GET /disclosure-offers` | the fixed links of every disclosure offer: per profile `client_id`, `request_uri` and the link (`openid4vp://` on `eudi`, the NL Wallet's universal link on `nl`). Public and readable from any origin; the developer portal shows them as QR codes |
| `POST /{v}/{p}/vp/response/{session}` | the `response_uri` (`direct_post.jwt`, form field `response`, or `error` when the wallet declines). See [Disclosure-based issuance](#disclosure-based-issuance) |
| `GET /healthz` | health |

Internal port (`ISSUER_INTERNAL_ADDR`, default `:8081`):

| Endpoint | |
|---|---|
| `POST /offers` | `{profile, credential_configuration_ids, subject, parameters, reference?}` → `201 {credential_offer, links}`. The issuer fetches the data from the bronpoort now; `404 no_data`, `503 source_unavailable` or `502 bad_source_data` when that fails, and nothing is stored. `reference` is the caller's own reference for revocation. The provider follows from the configurations: an offer with configurations of two providers is a `400 mixed_providers` |
| `POST /revocations` | `{reference}` revokes every credential issued for offers with that reference → `200 {revoked}`, and from then on nothing more is issued for it: an offer not yet redeemed gets `invalid_grant`, a token not yet used gets `credential_request_denied`. `{provider, list, index}` revokes one entry → 204. Anything else is a 400: a revocation cannot be undone, so nothing is guessed |
| `GET /healthz` | health |

## Disclosure-based issuance

The wallet discloses the BSN from its PID, and gets a Credential Offer for it in return, which it redeems with the pre-authorized code as above. Each disclosure offer (`disclosure_offers` in the configuration: a key, a provider, its credential configurations and the parameters) has a fixed link per wallet, which points at `request_uri` with the provider's `client_id` (`x509_hash:` of its access certificate):

- NL Wallet: `<universal link base>/disclosure_based_issuance?client_id=…&request_uri=…`;
- EUDI wallets: `openid4vp://?client_id=…&request_uri=…`.

The request carries the provider's registration certificate for the verifier role (`verifier_registration_certificate_file`, entitlement Service_Provider, authorizing the `bsn` of `urn:eudi:pid:nl:1`) in `verifier_info`: base64url-encoded on `nl`, the JWT itself on `eudi`, as each wallet reads it. The wallet fetches it with GET; `request_uri_method=post` is not offered.

The response is checked with `oid4vc-kit/oid4vp`: the PID must chain to `pid_trust_anchors` with the mDL signer EKU and an `exp`, be bound to the session by its key binding, disclose the `bsn` and nothing else, and be valid on its status list (signed under the PID signer's name; fetched over https without redirects). A session answers one response. The wallet gets:

| Outcome | Response |
|---|---|
| offer made | `200 {"redirect_uri": "openid-credential-offer://?credential_offer=…"}`: one use, the disclosure lifetime, no `tx_code` |
| the wallet declined (`error`) | `200 {}`; the session has ended and the source is not called |
| unknown, expired or used session; a response the verifier refuses | `400 invalid_request` |
| the source has no data | `404 no_issuable_attestations`, which the NL Wallet shows as "nothing to add" |
| the source or the PID status list is unreachable, or the source data cannot be issued | `500 server_error`; scanning again may work |

## Bronpoort

For each credential configuration the issuer posts to its `source_url`, with the offer parameters in the query. Which URL serves which configuration is configuration of the source adapter; the core only names the configuration. The body is the subject the offer caller named:

```json
{"subject": {"bsn": "999990019"}}
```

It is an object on purpose: the disclosure that nl-wallet's `issuance_server` sends is an array, so a backend can tell that no wallet presented a PID here. `eudi-adapter` accepts both and logs the matching Dataverwerking (`gbo-bsn-ontvangst-aanvrager` for this request, `gbo-pid-bsn-extractie` for a disclosure).

After a disclosure the issuer sends that array itself, in the form of nl-wallet's `issuance_server` v0.5.0 (plain attribute values; v0.6.0 tags each value as `{"type", "value"}`, which the backends do not read yet), with only the verified `bsn`:

```json
[{"id": "pid", "attestations": [{"attestation_type": "urn:eudi:pid:nl:1", "format": "dc+sd-jwt",
  "attributes": {"bsn": "999991772"}, "ca": "<CA of the PID signer>",
  "issuance_validity": {"signed": "…", "validFrom": "…", "validUntil": "…"}, "revocation_status": "Valid"}]}]
```

The response is the IssuableDocument list: `[{"id", "format": "dc+sd-jwt", "attestation_type": "<vct>", "attributes": {...}}]`. The offer is refused with `bad_source_data` when what the source returns cannot be issued:

- an attribute is not described by a claim of the credential configuration, or a mandatory claim is missing (the NL Wallet refuses undescribed attributes);
- a top-level attribute uses a name SD-JWT VC reserves (`iss`, `vct`, `exp`, `status`, ...);
- for `nl`: a number is not a whole number (the NL Wallet knows no others).

**The QTSP route does not run through pre-authorized offers.** The callback of `qtsp-reference` checks a PID the wallet disclosed against the pending ASI result; a subject named by an offer caller would make that check meaningless. It belongs with disclosure-based issuance, where the issuer passes on the PID its own verifier checked, but does not run there yet: the issuer asks only for the `bsn`, while the QTSP needs the name, birth date and nationalities and no BSN. Until disclosure offers name their own PID claims, the QTSP route stays on `eudi-issuance-server`.

## Run

```bash
cd services/eudi-credential-issuer
go test ./...
# PostgreSQL store tests:
ISSUER_TEST_DATABASE_URL=postgres://... go test ./internal/postgres/
```

| Environment | |
|---|---|
| `ISSUER_PUBLIC_URL` | external base URL, without trailing slash; issuer identifiers are `<public URL>/<provider>/<profile>` |
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
  "batch_size": 4, "offer_ttl": "5m", "token_ttl": "10m", "nonce_ttl": "5m", "status_list_size": 65536,
  "providers": [
    {"name": "belastingdienst",
     "access": {"key_file": "belastingdienst/access-key.pem", "chain_file": "belastingdienst/access-chain.pem"},
     "issuer": {"key_file": "belastingdienst/issuer-key.pem", "chain_file": "belastingdienst/issuer-chain.pem"},
     "status": {"key_file": "belastingdienst/status-key.pem", "chain_file": "belastingdienst/status-chain.pem"},
     "registration_certificate_file": "belastingdienst/wrprc.jwt",
     "request_encryption_key_file": "belastingdienst/request-encryption-key.pem",
     "credential_configurations": [
       {"id": "inkomensverklaring", "scope": "inkomensverklaring", "vct": "https://.../types/belastingdienst/inkomensverklaring/v2.4",
        "valid_days": 365, "source_url": "https://eudi-adapter/attestations/belastingdienst/inkomensverklaring",
        "status_list": "inkomensverklaring",
        "display": [{"name": "Inkomensverklaring", "locale": "nl"}],
        "claims": [{"path": ["belastingjaar"], "mandatory": true}]}
     ]}
  ],
  "profiles": [
    {"name": "nl", "kind": "nl", "wallet_provider_roots_file": "nl-wallet-providers.pem",
     "universal_link_base": "https://app.preproductie.wallet.edi.bzk.nl/deeplink"},
    {"name": "eudi", "kind": "eudi", "wallet_provider_roots_file": "eudi-wallet-providers.pem",
     "key_attestation_roots_file": "eudi-wallet-providers.pem",
     "key_storage": ["iso_18045_high"], "user_authentication": ["iso_18045_high"]}
  ]
}
```

In the demo stack this file is not written by hand: `eudi-adapter
materialize-credential-issuer` derives it from the active Source Release. Each
source becomes a provider with its provisioned issuer and status
certificates, its access certificate, its registration certificate and its
request encryption key (`make provision-wallet-registration`); each activated type becomes a credential
configuration `<source>-<type>` with the display and claims of the source
definition (a claim is mandatory unless the source marks it optional), 365
days of validity and the adapter's `/attestations/<source>/<type>` as
bronpoort. When `make provision-test-pid` has run, a test PID provider
`test-pid` follows, with one configuration `test-pid-pid` (`urn:eudi:pid:nl:1`)
fed by `test-pid-source` (`TEST_PID_SOURCE_URL`). A profile is configured only when its wallet providers are mounted
(`nl-wallet-providers.pem`, `eudi-wallet-providers.pem`, optionally
`eudi-key-attestation-roots.pem`); `eudi` also needs the key attestation
levels. Type Metadata (`type_metadata_uri`, `vct#integrity`) is not served
yet: wallets take the display from `credential_metadata`, which carries the
card name and claim labels but not the card description, logo or colours the
Type Metadata has. The command refuses a wallet registration that has expired
or no longer names every activated type, and replaces its output directory
(default `/runtime-issuer`) only when the new configuration is complete.

In Docker Compose the issuer runs with `--profile eudi --profile
credential-issuer`, beside `eudi-issuance-server`: a bootstrap job creates its
database in `postgres-eudi`, `credential-issuer-migrations` its tables,
`credential-issuer-materialize` its configuration (handed to the issuer's
user 1000), and `trust-host` serves the reader-root CRL, the WRPRC status list
and the pages a registration certificate names. The public port is
`GBO_PORT_CREDENTIAL_ISSUER` (9429); the internal API is published on
`127.0.0.1:9431` only, and is reachable without authentication from every
container in the `eudi` network. The tunnel routes are listed in
`docker-compose.cloudflare-tunnel.yml`. Start it with `make
credential-issuer-up`: it checks `ISSUER_PUBLIC_URL`, `EUDI_TRUST_PUBLIC_URL`
and `CREDENTIAL_ISSUER_DB_PASSWORD`, rematerializes the configuration and
recreates the issuer, which reads its configuration only at start. Run `make
provision-wallet-registration` first: it creates `.local/public-trust` as your
user, where Docker would otherwise create it as root.

The offer parameters (`POST /offers`, `parameters`) are passed to the bronpoort
as they are; the adapter accepts only the parameter sets of the offers the
source publishes (see the adapter's `/eudi-offers.json`), and a refusal there
reaches the offer caller as `503 source_unavailable`.

The issuer refuses to start with a configuration a wallet would refuse: no `scope`, `display` or claims, a scope used twice (HAIP section 4.3), an empty claim path, a mandatory claim that is an object (wallets check mandatory claims on leaves), a provider without its own access, issuer and status keys, a provider without credential configurations, a configuration of an unknown provider, a provider without registration certificate when an `nl` profile exists, a provider without a P-256 request encryption key when an `eudi` profile exists, a registration certificate that is not a JWT, whose `sub` is not the `organizationIdentifier` (2.5.4.97) of the provider's access certificate or that does not list each of the provider's `vct`s in `provides_attestations` (the NL Wallet v0.6 checks the binding to the access certificate, not yet the `vct`s; the issuer does not verify the signature), an `eudi` profile without key levels, provider, profile or status list names that are not unique lower-case letters, digits and hyphens, or the reserved name `status`, a public URL with a path, negative sizes or durations, a signing key that is not P-256, or a chain file with a self-signed certificate (HAIP: no trust anchor in `x5c`). Whitespace around the registration certificate in its file is trimmed. The status certificate must have the subject of the issuer certificate. The NL Wallet also expects the issuer certificate to carry EKU `1.0.18013.5.1.2` and the status certificate EKU `1.3.6.1.5.5.7.3.127`; the issuer does not check its own certificates.

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
- A credential is marked as issued only after it is signed, so a failed request leaves it issuable. A request for a credential that has been issued is refused before anything else happens, so it uses up no status list entries.
- A `client_id` in the token request must equal the `sub` of the Wallet Instance Attestation.
- The signing certificate of an incoming Wallet Instance Attestation or key attestation must not be self-signed (HAIP).
- For the `eudi` profile every key in a key attestation gets a credential (Appendix F.1: "SHOULD issue a Credential for each cryptographic public key specified in the attested_keys"); the number of keys is bounded by `batch_size`.
- Client authentication and DPoP are checked before the pre-authorized code is used, and a code is only redeemed on the profile of its offer, so a refused request does not burn the code. The attestation challenge is used up either way.
- DPoP `htu` is compared with the public URL, not the request URL, because the issuer runs behind a proxy.
- Server-provided DPoP nonces are not used: the NL Wallet does not retry on `use_dpop_nonce`. Replay of a DPoP proof is blocked by its `jti` within the 60-second `iat` window.
- The `iss` claim of a key proof is not checked against the client.
