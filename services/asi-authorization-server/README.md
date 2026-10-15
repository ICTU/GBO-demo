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
| User identification and authorization by the user (REQ-AZSP-6.1.3.1-02, -03; clause 6.1.3.3, steps 7-11) | one page shows the request and offers a login with the **NL Wallet** (OpenID4VP PID disclosure through nl-wallet's verification_server, cross-device) or with a **DigiD mock**, which checks nothing and provides the BSN only (stub, not conformant) |
| mTLS, PAR, refresh tokens | not offered (see below) |

## Endpoints

| Endpoint | |
|---|---|
| `GET /.well-known/oauth-authorization-server` | metadata (RFC 8414) |
| `GET /jwks.json` | the public token signing key |
| `GET /authorize` | authorization request: `response_type=code`, `client_id`, `redirect_uri`, `scope` (`verify`, `retrieve` or both), `state`, `code_challenge`, `code_challenge_method=S256`, `resource` (one data service of the ASIP). A valid request gets the request page |
| `POST /authorize/decision` | the user's choice on the request page: `transaction`, `decision` (`deny`, `digid` or `wallet`). Deny answers with the redirect to the client; `digid` leads to the DigiD mock; `wallet` opens a disclosure session and leads to the wallet page (refused when no verifier is configured, 503 when it is unreachable) |
| `GET /authorize/wallet` | the NL Wallet: the QR code of the disclosure session, refreshing itself; once the wallet disclosed the PID, the redirect to the client; when declined, expired or unusable, the request page again |
| `GET`, `POST /authorize/digid` | the DigiD mock: the login page, and the login with `transaction`, `username` (ignored) and `bsn`; a login answers with the redirect to the client |
| `POST /token` | token request: `grant_type=authorization_code`, `code`, `redirect_uri`, `code_verifier`, `client_assertion_type`, `client_assertion`, and a `DPoP` proof header |
| `POST /register` | client registration (RFC 7591): `redirect_uris`, `scope`, optionally `token_endpoint_auth_method`, and the `software_statement` |

## Run

```bash
make provision-asip-qtsp-certificate
cd services/asi-authorization-server
AZSP_TRUST_ANCHORS_FILE=../../.local/secrets/development-ca/reader-ca-cert.pem go run .
```

`make provision-asip-qtsp-certificate` issues a test QTSP certificate from the development reader CA, with the `organizationIdentifier` of the test QTSP in `testdata/register.json` (`NTRNL-90000001`). A test QTSP signs its software statement and its client assertions with its key.

Configuration: `AZSP_ADDR` (default `:4021`), `AZSP_ISSUER` (the public base URL, default `http://localhost:4021`), `AZSP_RESOURCES` (per data service of the ASIP its base URL and the name of its authentic source, as `url=name` separated by commas, default `http://localhost:4020/brp/authsrc-api=Basisregistratie Personen (mock)`; the request page shows the name, so it is required), `AZSP_SIGNING_KEY_FILE` (PEM, ECDSA P-256; without it the server generates a key at start, so tokens do not survive a restart), `AZSP_CLIENTS_FILE`, `AZSP_REGISTER_FILE` (default `testdata/register.json`), `AZSP_TRUST_ANCHORS_FILE` (PEM; the roots QTSP certificates must chain to, the development reader CA; without it every registration is refused), `AZSP_ENTITLEMENT_RECHECK_INTERVAL` (default `1h`).

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
- **Client.** The key of the certificate is the client key for `private_key_jwt`; the response returns it in `jwks` with its `kid`. `jwks` and `jwks_uri` in the request are refused; `token_endpoint_auth_method` must be `private_key_jwt` and is that when omitted. The scope is `verify`, `retrieve` or both; redirect URIs are absolute https URIs without fragment, or http on a loopback address (`localhost`, `127.0.0.1`, `[::1]`; RFC 8252 section 7.3), where browser and client run on the same machine, as the reference QTSP of the demo does.
- **Errors** (RFC 7591 section 3.2.2): `invalid_software_statement` for the statement and the certificate, `unapproved_software_statement` for the register, `invalid_client_metadata` and `invalid_redirect_uri` for the metadata. When the register cannot be consulted, a registration gets 503 with `temporarily_unavailable`.
- **Re-verification.** Every `AZSP_ENTITLEMENT_RECHECK_INTERVAL` the entitlement of every registered client is looked up again (clause 6.1.3.2, figure 2). A client that is no longer approved is deactivated: its token requests are `invalid_client`, its authorization requests get the error page; tokens already issued expire normally. When the register cannot be consulted, the client stays active until the next successful check. **That is a demo choice, to be reassessed for production.** The stub register reads its file on every lookup: remove `QEAA_Provider` from an entry in `AZSP_REGISTER_FILE` and the next check deactivates its clients, without a restart. A file that cannot be read or parsed counts as an unavailable register.
- **Storage.** Registered clients are kept in memory. After a restart of the authorization server a QTSP gets `invalid_client` and must register again; the reference client of slice 6 has to handle that. There is no client configuration endpoint (RFC 7592).

## Layout

Ports and adapters, as `AGENTS.md` asks for a service with an authorization boundary:

| Package | |
|---|---|
| `internal/authorization` | the core: the authorization, token and registration requests, PKCE, codes, client authentication, DPoP proofs, token issuance, re-verification. Ports `Clients`, `Register` and `Identifier`: an identification means identifies the user behind the port, and the core names the means in the token |
| `internal/clientconfig` | `Clients` from the configuration file |
| `internal/stubregister` | `Register`: the stub register |
| `internal/qtspcert` | the `provision-qtsp-certificate` command |
| `internal/walletverifier` | the NL Wallet as identification means: the client of the verifier's API (start a disclosure session, follow its status, read the disclosed PID), and the `provision-wallet-verifier` command: the reader certificate and configuration of the verifier (`services/asi-wallet-verifier`) |
| `internal/digidmock` | `Identifier`: the DigiD mock, an adapter with its login page, in two files so it can move to a service of its own |
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
| `acr`, `amr` | the means that identified the user (RFC 9068 section 2.2.3.1): `digid-mock` |
| `https://gbo.example/claims/bsn` | the BSN of the user, **in plain text** (D3). The BSN has no entry in the IANA JWT registry, so the claim name is collision-resistant (RFC 9068 section 2.2.2); the namespace is provisional (D8). Slice 9 of #336 protects it |
| `family_name`, `given_name`, `birthdate`, `place_of_birth`, `nationalities` | only when the means provides them, which DigiD does not (D7): the mandatory PID attributes of the user (CIR (EU) 2024/2977), under the names of the IANA JWT Claims Registry (RFC 9068 section 2.2.2), which the SD-JWT VC encoding of the PID rulebook uses too. `place_of_birth` is an object with `country`, `region` and/or `locality`; `nationalities` an array of ISO 3166-1 alpha-2 codes |

The ASIP uses only the BSN; it does not check the PID claims.

## The request page and the DigiD mock

A valid authorization request gets one page, in Dutch, with a banner that it is a test environment without real identification. It names the QTSP by its legal name (the register entry for a registered client, taken again at every re-verification; `client_name` in the configuration; else the `client_id`), the operation it asks (`verify`, `retrieve`) and the authentic source. The user logs in with the NL Wallet or DigiD (mock), or denies: Weigeren gives `access_denied`. Without a configured verifier the NL Wallet is shown but disabled.

Choosing a means starts the approval; the grant is established only after the login succeeds (RFC 6749 section 3.1; ETSI TS 119 478 figure 3, step 9). There is no second confirmation page.

- The DigiD mock (`internal/digidmock`) is an adapter of the core's `Identifier` port: the web layer passes on what the user typed, and the core has the means identify the user and names the means itself. Its page is in the style of DigiD, like the toestemmingsportaal's: the username is ignored and the password field carries the BSN. A BSN that is not nine digits keeps the page open with an error; there is no eleven-test, because not every BSN of the demo's test data passes it. A nine-digit BSN the source does not know logs in, and the ASIP answers `Unknown`. It is not real DigiD.
- With DigiD the token carries the BSN only, with `acr`/`amr` `digid-mock`: DigiD provides no further identification data. Read literally, REQ-AZSP-6.1.3.1-08 asks for the identification data of the user; this is a GBO interpretation (D7).
- The request waits as a transaction: kept server-side, valid for 5 minutes, used once, and bound to the browser by an HttpOnly, SameSite=Strict cookie set with the page. A choice or login for an unknown, used or expired transaction, or without the cookie, gets an error page, not a redirect.
- The pages are not cached and may not be framed.

## The NL Wallet

With the NL Wallet the authorization server has the citizen disclose the PID through nl-wallet's `verification_server` (`services/asi-wallet-verifier`), behind the core's `SessionIdentifier` port (`internal/walletverifier`):

- choosing the wallet opens a disclosure session for usecase `asi-login` that asks `bsn`, `family_name`, `given_name`, `birthdate` and `nationalities` of `urn:eudi:pid:nl:1`. The core keeps the session with the transaction and never takes one from the browser, so a finished session cannot be carried over to another transaction (the QR code shows the session);
- the wallet page shows the QR code (cross-device) and refreshes itself every two seconds; it has no script, so the strict CSP stays (it allows `data:` images);
- when the wallet disclosed the PID, the core logs the user in: the token carries the BSN, the four PID attributes and `acr`/`amr` `nl-wallet`. The NL PID has no place of birth, so one of the five mandatory PID attributes is missing;
- declined in the wallet or expired, or a disclosure without a BSN or of another type: the request page again, with why, and the transaction stays. An unreachable verifier: a 503 page, and the transaction stays for DigiD (mock).

Configuration: `AZSP_WALLET_VERIFIER_URL` (internal API), `AZSP_WALLET_VERIFIER_STATUS_URL` (wallet API, for the session status) and `AZSP_WALLET_VERIFIER_API_KEY_FILE`; without the key file the wallet is not offered.

## Interpretations and choices

- **One mechanism of each.** REQ-AZSP-6.1.3.1-05 and -06 require mTLS or `private_key_jwt`, and mTLS or DPoP. This server offers `private_key_jwt` and DPoP. A production deployment should consider mTLS.
- **No PAR.** The FAPI 2.0 Security Profile requires pushed authorization requests; ETSI TS 119 478 only asks to "take into account" FAPI 2.0 (REQ-AZSP-6.1.3.4-01). A stricter server than ETSI would exclude QTSPs that implement ETSI only.
- **Algorithms.** Tokens are signed with ES256. Client assertions may use ES256 or PS256, DPoP proofs ES256. RFC 9068 section 2.1 requires resource servers to support RS256, which FAPI 2.0 does not allow; the ASIP accepts it, this server never uses it.
- **Authorization per operation**, not per attribute: ETSI TS 119 478 defines the scopes `verify` and `retrieve` and no binding of a token to attributes.
- **Resource indicator.** `resource` (RFC 8707) is required on the authorization request and names exactly one data service of the ASIP: with several there is no default, and a token is for one data service, its `aud`. A token request may repeat it or leave it out. Anything else is `invalid_target`. A QTSP that verifies attributes of two authentic sources authorizes twice, and the request page names the authentic source of each.
- **Codes** are single-use and valid for 60 seconds; a code presented by another client or with another `redirect_uri` is consumed and refused.
- **Clock leeway**: 30 seconds on client assertions; DPoP proofs are accepted within 60 seconds of their `iat` and only once.
- No log line contains the BSN.
