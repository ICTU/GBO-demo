# qtsp-reference — reference QTSP for the ASI route (ETSI TS 119 478)

The client side of the Authentic Source Interface: a qualified trust service provider (QTSP) that has an ASIP verify or retrieve attributes of a user. It is the demo's reference client and a place where a user starts. The citizen logs in at the QTSP before a request starts. It is not a real QTSP: it issues no attestation. Part of epic #336, slice 6 (#533) and slice 14 (#560).

## What it does

It discovers its way rather than being configured with URLs:

1. **Discover** (clause 5.3): the Discover Interface (`QTSP_DIP_URL`, the demo's `asi-discovery-stub`) gives the `dataService` record of each requested attribute. All attributes of one request must belong to one data service, because a token is for one data service.
2. The record's `endpointDescription` is the data service's **contract**. Its server must be the record's `endpointURI`. Its `x-oauth-authorization-server` names the **authorization server's metadata** (RFC 8414), and the issuer must be the prefix of that URL.
3. **Register** (RFC 7591) with a software statement sealed with the QTSP certificate, carrying its `registryURI`. This happens once per authorization server; the web side does it at start.
4. **Authorize**: the authorization code flow with PKCE S256, `state`, the operation as scope, `resource` = the record's `endpointURI`, and RFC 9396 `authorization_details` containing that resource, operation and the selected attribute identifiers.
5. **Token**: `private_key_jwt` with the certificate key, and a DPoP proof. The token is DPoP-bound.
6. **Verify or retrieve** with the token and a DPoP proof, in a W3C trace of its own (`traceparent`), which the ASIP's spans join. A 200 is accepted only with a valid **seal**: the detached JWS in `X-JWS-Signature` over the exact body, whose certificate chain leads to `QTSP_SEAL_TRUST_ANCHORS`. Otherwise the result is reported as not accepted.

## Login

The QTSP identifies the citizen itself before a request starts (eIDAS Article 24(1)). It asks the name, the date of birth and the nationalities, never the BSN: a QTSP may not process it. Two means:

- **NL Wallet**: a disclosure of the NL PID (`urn:eudi:pid:nl:1`: `family_name`, `given_name`, `birthdate`, `nationalities`) through usecase `qtsp-login` of the wallet verifier (`services/asi-wallet-verifier`), with the QTSP's own reader certificate. `POST /login/wallet` starts the session and keeps it with this browser's login; `GET /login/wallet` shows the QR code and refreshes itself until the wallet has disclosed. Declined or expired, or a disclosure without all four attributes: the login page with the reason. The verifier unreachable: a 503 page, and the mock still works. Without a configured verifier the wallet button is disabled.
- **Test data (mock)**: `POST /login/mock` with the four attributes (date of birth as YYYY-MM-DD, nationalities as country codes separated by commas). It checks nothing else; it exists for demos and CI.

A login is kept in memory behind an HttpOnly, SameSite=Lax cookie for 30 minutes and serves several requests; `POST /logout` ends it. The pages name the citizen ("Ingelogd als …"). The QTSP logs no name, date of birth or nationality.

**Demo shortcut**: the QTSP shares the verifier and its API key with the authorization server. In production each relying party has its own verifier and keys.

## Web

`GET /` shows the login page without a login, and otherwise a form: verify or retrieve, and the five BRP attributes of the demo with the values of test person Frouke Jansen as examples. `POST /start` (with a login; otherwise back to the login page) plans, registers if needed, and sends the browser to the authorization server. It keeps `state` and the PKCE verifier in a session behind an HttpOnly, SameSite=Lax cookie (in memory, 10 minutes). `GET /callback` checks the session and `state`, takes the token, asks the ASIP, and shows the result per attribute with the seal check. A denial shows "Geweigerd"; a callback without a matching session or `state` is refused before any token request.

### Request API

The developer portal lays a request down here and follows it, while the user still goes through the QTSP:

- `GET /api/attributes`: the attributes the form offers, with an example value.
- `POST /api/requests` with `{"operation":"verify","attributes":[{"identifier":"…","value":{…}}]}` (for `retrieve`, without values), and optionally `login_hint` with test data for the mock login (`family_name`, `given_name`, `birthdate`, `nationalities`), which stays on the server. The QTSP checks the request, finds the data service and registers, as the form does, and answers 201 with an `id` and a `start_url`. A request it does not start gets an RFC 9457 problem with the reason: 400 for an invalid request, 422 when no data service provides the attributes, 502 when discovery or registration fails.
- `start_url` (`/requests/{id}/start`) shows the login page when the browser has no login, with the mock prefilled from `login_hint`; after the login the browser returns to it. With a login it sends the browser to the authorization server. It carries only the id, so no value appears in a URL, and it works once (410 after that). The user returns to the result page.
- `GET /api/requests/{id}`: `pending` until the user returns, then `done` (the result per attribute, the seal check, and `trace_id`: the trace of the call at the ASIP), `denied` (the OAuth error) or `failed` (an error answer of the ASIP, or no token).

A request and its outcome can be fetched for 10 minutes; then it is gone (404). The API has no access control, like the other mocks of the demo: anyone who can reach the port can lay a request down.

The authorization server allows an http redirect URI on a loopback address only, so the web side works with the browser on the same machine (`http://localhost:9427`).

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
| `internal/web` | the web side, the login and the request API |
| `internal/urlmap` | the transport map from public to internal URLs |
| `main.go` | composition root |

`e2e_test.go` builds the authorization server, the ASIP and the DIP stub and starts them, with a fake Outway in front of the BRP. Through them it runs every verification result, the error answers 400, 401, 404 and 501, retrieve, a seal from another anchor, a refused registration, a denial, the web side from login and form to result, and the request API with the login (`api_e2e_test.go`). `internal/web/login_test.go` tests the wallet login against a fake verifier.
