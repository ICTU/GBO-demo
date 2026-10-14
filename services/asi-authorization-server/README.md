# asi-authorization-server — authorization server of the ASIP mock (ETSI TS 119 478 clause 6.1.3)

The authorization server (I4 Authorize) of the ASIP mock in [`services/asi-provider`](../asi-provider). A qualified trust service provider (QTSP) obtains a DPoP-bound JWT access token here and presents it to the ASIP for `/verify` or `/retrieve`. Part of epic #336; this is slice 2 (#503).

The ASIP and the authorization server may be operated by one party, but they are two roles (ASIP and AZSP). They are separate services with separate keys: the ASIP only knows the issuer and fetches the signing keys through the metadata.

## What is real and what is not

| Part | Status |
|---|---|
| Authorization code flow (RFC 6749 section 4.1) with PKCE, S256 only (REQ-AZSP-6.1.3.1-01, -04) | implemented |
| Client authentication with `private_key_jwt` (REQ-AZSP-6.1.3.1-05) | implemented; the only method offered |
| DPoP-bound access tokens (REQ-AZSP-6.1.3.1-06) | implemented; every token is bound |
| JWT access tokens per RFC 9068 (REQ-AZSP-6.1.3.1-08) | implemented; contents below |
| Metadata (RFC 8414) and JWKS | implemented |
| Dynamic client registration with a sealed software statement (REQ-AZSP-6.1.3.2-01) | implemented; see below. Clients can also be registered in configuration |
| Register of wallet-relying parties (CIR (EU) 2025/848, D9) | **stub**: entries from `testdata/register.json`, read again on every lookup; the national register and its API (ETSI/EUDI TS5) are not available to this project |
| QTSP certificates (REQ-AZSP-6.1.3.2-02) | **test certificates** from the development reader CA, not Wallet-Relying Party Access Certificates according to ETSI TS 119 411-8 |
| User identification and authorization by the user (REQ-AZSP-6.1.3.1-02; clause 6.1.3.3, step 7) | **stub, not conformant**: a page where the user picks a test person and approves or denies the request; nothing is checked. Slice 7 adds OpenID4VP PID disclosure with the NL Wallet |
| mTLS, PAR, refresh tokens | not offered (see below) |

## Endpoints

| Endpoint | |
|---|---|
| `GET /.well-known/oauth-authorization-server` | metadata (RFC 8414) |
| `GET /jwks.json` | the public token signing key |
| `GET /authorize` | authorization request: `response_type=code`, `client_id`, `redirect_uri`, `scope` (`verify`, `retrieve` or both), `state`, `code_challenge`, `code_challenge_method=S256`, `resource` (one data service of the ASIP), optionally `login_hint`. A valid request gets the test-person chooser |
| `POST /authorize/decision` | the user's decision on the chooser: `transaction`, `person`, `decision` (`approve` or `deny`); answers with the redirect to the client |
| `POST /token` | token request: `grant_type=authorization_code`, `code`, `redirect_uri`, `code_verifier`, `client_assertion_type`, `client_assertion`, and a `DPoP` proof header |
| `POST /register` | client registration (RFC 7591): `redirect_uris`, `scope`, optionally `token_endpoint_auth_method`, and the `software_statement` |

## Run

```bash
make provision-asip-qtsp-certificate
cd services/asi-authorization-server
AZSP_TRUST_ANCHORS_FILE=../../.local/secrets/development-ca/reader-ca-cert.pem go run .
```

`make provision-asip-qtsp-certificate` issues a test QTSP certificate from the development reader CA, with the `organizationIdentifier` of the test QTSP in `testdata/register.json` (`NTRNL-90000001`). A test QTSP signs its software statement and its client assertions with its key.

Configuration: `AZSP_ADDR` (default `:4021`), `AZSP_ISSUER` (the public base URL, default `http://localhost:4021`), `AZSP_RESOURCES` (the base URLs of the ASIP's data services, separated by commas, default `http://localhost:4020/brp/authsrc-api`), `AZSP_SIGNING_KEY_FILE` (PEM, ECDSA P-256; without it the server generates a key at start, so tokens do not survive a restart), `AZSP_CLIENTS_FILE`, `AZSP_TEST_PERSONS_FILE` (default `testdata/test-persons.json`), `AZSP_REGISTER_FILE` (default `testdata/register.json`), `AZSP_TRUST_ANCHORS_FILE` (PEM; the roots QTSP certificates must chain to, the development reader CA; without it every registration is refused), `AZSP_ENTITLEMENT_RECHECK_INTERVAL` (default `1h`).

A clients file registers each client with its redirect URIs, its scopes and the public keys of its client assertions:

```json
{
  "clients": [
    {
      "client_id": "test-qtsp",
      "redirect_uris": ["https://qtsp.example/callback"],
      "scope": "verify retrieve",
      "jwks": { "keys": [ { "kty": "EC", "crv": "P-256", "x": "…", "y": "…", "kid": "test-qtsp-key" } ] }
    }
  ]
}
```

There is no client to try the flow by hand yet; the reference QTSP client is slice 6 of #336. `internal/httpapi/e2e_test.go` runs the whole flow against a started `asi-provider`.

## Dynamic client registration

A QTSP registers once per authorization server, before its first authorization request, and keeps the `client_id` it gets back (RFC 7591). It finds the registration endpoint in the metadata.

- **Software statement.** The QTSP signs it with the key of its X.509 certificate, ES256 or PS256, the chain in `x5c` (REQ-TSP-6.1.3.2-01). It must contain `iss`, `registryURI`, `iat` and `exp`; it must not be expired or issued in the future. There is no maximum lifetime: a replayed statement registers a client nobody can authenticate without the certificate key. Metadata in the statement take precedence over plain JSON members (RFC 7591 section 3.1.1).
- **Certificate.** It must chain to a trust anchor, be valid now, allow digital signatures and carry an `organizationIdentifier`.
- **Register.** The entry `registryURI` points to must carry the entitlement `https://uri.etsi.org/19475/Entitlement/QEAA_Provider` (Article 45e concerns QTSPs issuing qualified electronic attestations of attributes) and have the `organizationIdentifier` of the certificate among its identifiers. TS5 also uses `http://data.europa.eu/eudi/entitlement/...` in an example; only the form of its table is accepted.
- **Client.** The key of the certificate is the client key for `private_key_jwt`; the response returns it in `jwks` with its `kid`. `jwks` and `jwks_uri` in the request are refused; `token_endpoint_auth_method` must be `private_key_jwt` and is that when omitted. The scope is `verify`, `retrieve` or both; redirect URIs are absolute https URIs without fragment.
- **Errors** (RFC 7591 section 3.2.2): `invalid_software_statement` for the statement and the certificate, `unapproved_software_statement` for the register, `invalid_client_metadata` and `invalid_redirect_uri` for the metadata. When the register cannot be consulted, a registration gets 503 with `temporarily_unavailable`.
- **Re-verification.** Every `AZSP_ENTITLEMENT_RECHECK_INTERVAL` the entitlement of every registered client is looked up again (clause 6.1.3.2, figure 2). A client that is no longer approved is deactivated: its token requests are `invalid_client`, its authorization requests get the error page; tokens already issued expire normally. When the register cannot be consulted, the client stays active until the next successful check. **That is a demo choice, to be reassessed for production.** The stub register reads its file on every lookup: remove `QEAA_Provider` from an entry in `AZSP_REGISTER_FILE` and the next check deactivates its clients, without a restart. A file that cannot be read or parsed counts as an unavailable register.
- **Storage.** Registered clients are kept in memory. After a restart of the authorization server a QTSP gets `invalid_client` and must register again; the reference client of slice 6 has to handle that. There is no client configuration endpoint (RFC 7592).

## Layout

Ports and adapters, as `AGENTS.md` asks for a service with an authorization boundary:

| Package | |
|---|---|
| `internal/authorization` | the core: the authorization, token and registration requests, PKCE, codes, client authentication, DPoP proofs, token issuance, re-verification. Ports `Clients`, `Identifier` and `Register` |
| `internal/clientconfig` | `Clients` from the configuration file |
| `internal/stubregister` | `Register`: the stub register |
| `internal/qtspcert` | the `provision-qtsp-certificate` command |
| `internal/testpersons` | `Identifier`: the test persons of the chooser |
| `internal/httpapi` | the endpoints, and the translation to the responses of RFC 6749 |
| `main.go` | composition root |

## The access token

A JWT according to RFC 9068, signed with ES256, `typ` `at+jwt`, with:

| Claim | |
|---|---|
| `iss`, `aud`, `exp`, `iat`, `jti`, `client_id` | RFC 9068 section 2.2; `aud` is the ASIP base URL, the lifetime is 10 minutes |
| `sub` | an opaque value per token, so two tokens for one user cannot be linked through it (D3) |
| `scope` | `verify`, `retrieve` or both: the operations ETSI TS 119 478 authorizes (clause 6.1.3.2) |
| `cnf.jkt` | the thumbprint of the client's DPoP key (RFC 9449 section 6) |
| `https://gbo.example/claims/bsn` | the BSN of the user, **in plain text** (D3). The BSN has no entry in the IANA JWT registry, so the claim name is collision-resistant (RFC 9068 section 2.2.2); the namespace is provisional (D8). Slice 9 of #336 protects it |
| `family_name`, `given_name`, `birthdate`, `place_of_birth`, `nationalities` | the mandatory PID attributes of the user (CIR (EU) 2024/2977), under the names of the IANA JWT Claims Registry (RFC 9068 section 2.2.2), which the SD-JWT VC encoding of the PID rulebook uses too. `place_of_birth` is an object with `country`, `region` and/or `locality`; `nationalities` an array of ISO 3166-1 alpha-2 codes |

The ASIP uses only the BSN; it does not check the PID claims.

## The test-person chooser

A valid authorization request gets a page, in Dutch, with a banner that it is a test environment without real identification. The user picks a test person and approves or denies the request for the operation the client asks (`verify`, `retrieve`). Deny gives `access_denied`; approve gives the code. `login_hint` only preselects a test person.

- The request waits as a transaction: kept server-side, valid for 5 minutes, used once, and bound to the browser by an HttpOnly, SameSite=Strict cookie set with the page. A decision for an unknown, used or expired transaction, without the cookie, or for an unknown test person gets an error page, not a redirect.
- The page is not cached and may not be framed.
- The test persons are in `AZSP_TEST_PERSONS_FILE` (default `testdata/test-persons.json`) with their BSN and the five PID attributes; they equal the BRP mock, the source the ASIP asks. A person without all of them stops the server at start.

## Interpretations and choices

- **One mechanism of each.** REQ-AZSP-6.1.3.1-05 and -06 require mTLS or `private_key_jwt`, and mTLS or DPoP. This server offers `private_key_jwt` and DPoP. A production deployment should consider mTLS.
- **No PAR.** The FAPI 2.0 Security Profile requires pushed authorization requests; ETSI TS 119 478 only asks to "take into account" FAPI 2.0 (REQ-AZSP-6.1.3.4-01). A stricter server than ETSI would exclude QTSPs that implement ETSI only.
- **Algorithms.** Tokens are signed with ES256. Client assertions may use ES256 or PS256, DPoP proofs ES256. RFC 9068 section 2.1 requires resource servers to support RS256, which FAPI 2.0 does not allow; the ASIP accepts it, this server never uses it.
- **Authorization per operation**, not per attribute: ETSI TS 119 478 defines the scopes `verify` and `retrieve` and no binding of a token to attributes.
- **Resource indicator.** `resource` (RFC 8707) is required on the authorization request and names exactly one data service of the ASIP: with several there is no default, and a token is for one data service, its `aud`. A token request may repeat it or leave it out. Anything else is `invalid_target`. A QTSP that verifies attributes of two authentic sources authorizes twice.
- **Codes** are single-use and valid for 60 seconds; a code presented by another client or with another `redirect_uri` is consumed and refused.
- **Clock leeway**: 30 seconds on client assertions; DPoP proofs are accepted within 60 seconds of their `iat` and only once.
- No log line contains the BSN.
