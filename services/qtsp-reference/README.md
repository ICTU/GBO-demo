# qtsp-reference — reference QTSP for the ASI route (ETSI TS 119 478)

The client side of the Authentic Source Interface: a qualified trust service provider (QTSP) that has an ASIP verify or retrieve attributes of a user. It is the demo's reference client and a place where a user starts. The citizen logs in at the QTSP before a request starts. It is not a real QTSP: it issues only a clearly marked, non-qualified test statement. Part of epic #336.

## What it does

It discovers its way rather than being configured with URLs:

1. **Discover** (clause 5.3): the Discover Interface (`QTSP_DIP_URL`, the demo's `asi-discovery-stub`) gives the `dataService` record of each requested attribute. All attributes of one request must belong to one data service, because a token is for one data service.
2. The record's `endpointDescription` is the data service's **contract**. Its server must be the record's `endpointURI`. Its `x-oauth-authorization-server` names the **authorization server's metadata** (RFC 8414), and the issuer must be the prefix of that URL.
3. **Register** (RFC 7591) with a software statement sealed with the QTSP certificate, carrying its `registryURI`. This happens once per authorization server; the web side does it at start.
4. **Authorize**: the authorization code flow with PKCE S256, `state`, the operation as scope, `resource` = the record's `endpointURI`, and RFC 9396 `authorization_details` containing that resource, operation and the selected attribute identifiers.
5. **Token**: `private_key_jwt` with the certificate key, and a DPoP proof. The token is DPoP-bound.
6. **Verify or retrieve** with the token and a DPoP proof, in a W3C trace of its own (`traceparent`), which the ASIP's spans join. A 200 is accepted only with a valid **seal**: the detached JWS in `X-JWS-Signature` over the exact body, whose certificate chain leads to `QTSP_SEAL_TRUST_ANCHORS`. Otherwise the result is reported as not accepted.
7. **Issue a test statement to the NL Wallet** after every requested verification returned `Match` or `MatchWithVariation`, or every requested value was retrieved. The result page shows the static QR code of usecase `qtsp-attestation`. The central issuance server asks the QTSP for the pending statement over internal HTTPS and signs it with the QTSP's own issuer certificate.

## Login

The QTSP identifies the citizen itself before a request starts (eIDAS Article 24(1)). It asks the name, the date of birth and the nationalities, never the BSN: a QTSP may not process it. Two means:

- **NL Wallet**: a disclosure of the NL PID (`urn:eudi:pid:nl:1`: `family_name`, `given_name`, `birthdate`, `nationalities`) through usecase `qtsp-login` of the wallet verifier (`services/asi-wallet-verifier`), with the QTSP's own reader certificate. `POST /login/wallet` starts the session and keeps it with this browser's login; `GET /login/wallet` shows the QR code and refreshes itself until the wallet has disclosed. Declined or expired, or a disclosure without all four attributes: the login page with the reason. The verifier unreachable: a 503 page, and the mock still works. Without a configured verifier the wallet button is disabled.
- **Test data (mock)**: prefilled with test person Frouke Jansen, or with the request's `login_hint`. `POST /login/mock` with the four attributes (date of birth as YYYY-MM-DD, nationalities as country codes separated by commas). It checks nothing else; it exists for demos and CI.

A login is kept in memory behind an HttpOnly, SameSite=Lax cookie for 30 minutes and serves several requests; `POST /logout` ends it. The pages name the citizen ("Ingelogd als …"). The QTSP logs no name, date of birth or nationality.

**Demo shortcut**: the QTSP shares the verifier and its API key with the authorization server. In production each relying party has its own verifier and keys.

## Web

The pages have a look of their own, a private party in green and teal, so that a viewer tells the QTSP apart from the authorization server and the government portals of the demo. One style sheet in the templates, no script, no external fonts.


`GET /` shows the login page without a login, and otherwise the request page: "Wat wilt u laten verifiëren?" with products as cards, one per request (a token is for one data service). **Adres** verifies the BRP address attribute; its fields (street, house number, postal code, city, country) are filled in one by one, prefilled with the address of test person Frouke Jansen, and the QTSP composes the attribute value from them. **Standplaatsvergunning** is shown as coming soon: no source of market stall permits exists yet. Name, given names, birth date and nationality are no products: the QTSP has them from the citizen's PID. The page only verifies; retrieval, and every attribute, stay available through the request API below. Without a product, with a product that is not available, or with an empty field (all address fields are required: the ASIP compares the whole address), the page comes back with the message and the typed values. `POST /start` (with a login; otherwise back to the login page) plans, registers if needed, and sends the browser to the authorization server. It keeps `state` and the PKCE verifier in a session behind an HttpOnly, SameSite=Lax cookie (in memory, 10 minutes). `GET /callback` checks the session and `state`, takes the token, asks the ASIP, and shows the result per attribute with the seal check. A denial shows "Geweigerd"; a callback without a matching session or `state` is refused before any token request.

### Request API

The developer portal lays a request down here and follows it, while the user still goes through the QTSP:

- `GET /api/attributes`: the attributes the form offers, with an example value.
- `POST /api/requests` with `{"operation":"verify","attributes":[{"identifier":"…","value":{…}}]}` (for `retrieve`, without values), and optionally `login_hint` with test data for the mock login (`family_name`, `given_name`, `birthdate`, `nationalities`), which stays on the server. The QTSP checks the request, finds the data service and registers, as the form does, and answers 201 with an `id` and a `start_url`. A request it does not start gets an RFC 9457 problem with the reason: 400 for an invalid request, 422 when no data service provides the attributes, 502 when discovery or registration fails.
- `start_url` (`/requests/{id}/start`) shows the login page when the browser has no login, with the mock prefilled from `login_hint`; after the login the browser returns to it. With a login it sends the browser to the authorization server. It carries only the id, so no value appears in a URL, and it works once (410 after that). The user returns to the result page.
- `GET /api/requests/{id}`: `pending` until the user returns, then `done` (the result per attribute, the seal check, and `trace_id`: the trace of the call at the ASIP), `denied` (the OAuth error) or `failed` (an error answer of the ASIP, or no token).

A request and its outcome can be fetched for 10 minutes; then it is gone (404). The API has no access control, like the other mocks of the demo: anyone who can reach the port can lay a request down.

The authorization server allows an http redirect URI on a loopback address only, so the web side works with the browser on the same machine (`http://localhost:9427`).

## Wallet issuance

An accepted result is kept in memory for at most ten minutes and is removed after its first issuance. The wallet discloses `family_name`, `given_name`, `birthdate` and `nationalities` from its PID. These must equal the identity from the QTSP login; nationality order does not matter. A newer successful request for the same identity replaces the previous pending request. No match, an expired or already used result returns an empty document list. This is a demo binding and is not a unique civil identity check.

The issued SD-JWT VC has VCT `https://gbo.ictu.nl/credentials/qtsp-attestation/v1`, is valid for 30 days and contains the subject's name and birth date, the accepted result per attribute, the source's legal name, the issuer and verification date. The issuance server creates four cryptographic copies for unlinkability and Proof of Association, as it does for the source attestations; the wallet presents these as one card. For `MatchWithVariation` it records the value submitted by the QTSP alongside that outcome; the ASIP response does not provide the differing source value, so the statement does not present the submitted value as an exact source match. It always states that it is a test and not a qualified electronic attestation of attributes. `NoMatch` and `Unknown` never create a pending statement.

The existing central `eudi-issuance-server` remains unmodified. Its generated configuration adds a separate usecase and QTSP reader, issuer and status-list certificates. The QTSP callback listens only on the Compose networks under `https://qtsp-reference:4443/attestations/<secret>`; the secret path is generated with the certificates and shared only through their mounted directory. Its server certificate chains to the development issuer CA pinned by the issuance server. The path prevents other containers from calling the obvious endpoint accidentally, but it is not a replacement for client authentication. There is no client-certificate support in the configured callback client, so the internal network boundary remains a deliberate demo shortcut.

Missing QTSP issuance certificates do not take down either route: the QTSP logs a warning and continues without the wallet action, while the central materializer warns and emits the ordinary source-issuance configuration. A partial certificate set remains a startup error. `make demo-asi` provisions the set when `EUDI_PUBLIC_URL` is configured.

## CLI

The same flow from the command line. The test harness plays the user at the authorization server: it logs in with the DigiD mock with `--bsn` (default `999991772`, Frouke Jansen), or denies; a real user does that in a browser.

```bash
qtsp-reference verify --attr 'https://gbo.example/attributes/family_name/1={"family_name":"Jansen"}' --bsn 999991772
qtsp-reference retrieve --attr https://gbo.example/attributes/address/1 --bsn 999991772
```

Test-harness options provoke the error answers of the acceptance criteria:
- `--malformed`: a malformed body (400);
- `--tamper-token`: a tampered token (401);
- `--extra-attribute <id>`: an attribute that was not discovered (404);
- `--mandate`: a claim to act for another person (501);
- `--deny`: the user denies.

The exit code is 0 for an accepted result, 2 for an error answer of the ASIP, 3 for a result whose seal is invalid, 4 for a denial, and 1 for other failures. The CLI prints no token and no BSN.

In compose:

```bash
docker compose exec qtsp-reference ./qtsp-reference verify --attr 'https://gbo.example/attributes/family_name/1={"family_name":"Jansen"}'
```

## Run

`make demo-asi` starts it on `GBO_PORT_QTSP` (default 9427), with a test QTSP certificate from the development reader CA (`make provision-asip-qtsp-certificate`, entered in the authorization server's stub register).

Configuration:
- `QTSP_ADDR` (default `:4023`);
- `QTSP_PUBLIC_URL` (the base of the redirect URI, default `http://localhost:4023`);
- `QTSP_DIP_URL` (required);
- `QTSP_KEY_FILE` and `QTSP_CERT_FILE` (required: the certificate key and chain, leaf first);
- `QTSP_REGISTRY_URI` (default the test QTSP's entry);
- `QTSP_SEAL_TRUST_ANCHORS` (required: the roots of seal certificates);
- `QTSP_WALLET_VERIFIER_URL` (the verifier's internal API), `QTSP_WALLET_VERIFIER_STATUS_URL` (its wallet API, for the session status) and `QTSP_WALLET_VERIFIER_API_KEY_FILE`; without the key file only the mock login is offered;
- `EUDI_PUBLIC_URL` and `EUDI_UL_BASE`, used to construct the static wallet issuance link; without `EUDI_PUBLIC_URL` no issuance action is shown;
- `QTSP_ISSUANCE_ADDR` (default `:4443`), `QTSP_ISSUANCE_TLS_KEY_FILE`, `QTSP_ISSUANCE_TLS_CERT_FILE` and `QTSP_ISSUANCE_CALLBACK_PATH_FILE`, for the internal HTTPS callback;
- `QTSP_URL_MAP`: `public=internal,…`. The records and contracts name public URLs, which a container cannot reach. The map changes the transport only; DPoP proofs and audiences keep the public URLs.

If the authorization server refuses the registration (for example, no `QEAA_Provider` entitlement), the QTSP stops with the reason. While discovery or the server is unavailable, it tries again.

## Layout

| Package | |
|---|---|
| `internal/qtsp` | the flow: discover, register, authorize, token, verify or retrieve, and the seal check |
| `internal/seal` | validation of the seal on a result |
| `internal/harness` | the test harness at the authorization server (request page and DigiD mock) |
| `internal/cli` | the CLI: options, one run, the report |
| `internal/login` | who the citizen is: the identity, the wallet port, the mock's checks |
| `internal/walletlogin` | the NL Wallet as login means, through the wallet verifier |
| `internal/issuance` | transport-independent short-lived identity binding and one-time statement creation |
| `internal/issuancecallback` | the issuance-server HTTPS callback adapter |
| `internal/web` | the web side, the login and the request API |
| `internal/urlmap` | the transport map from public to internal URLs |
| `main.go` | composition root |

`e2e_test.go` builds the authorization server, the ASIP and the DIP stub and starts them, with a fake Outway in front of the BRP. Through them it runs every verification result, the error answers 400, 401, 404 and 501, retrieve, a seal from another anchor, a refused registration, a denial, the web side from login and form to result, and the request API with the login (`api_e2e_test.go`). `internal/web/login_test.go` tests the wallet login against a fake verifier.
