# qtsp-reference — reference QTSP for the ASI route (ETSI TS 119 478)

The client side of the Authentic Source Interface: a qualified trust service provider (QTSP) that has an ASIP verify or retrieve attributes of a user. It is the demo's reference client and a place where a user starts. It is not a real QTSP: it issues no attestation. Part of epic #336, slice 6 (#533).

## What it does

It discovers its way rather than being configured with URLs:

1. **Discover** (clause 5.3): the Discover Interface (`QTSP_DIP_URL`, the demo's `asi-discovery-stub`) gives the `dataService` record of each requested attribute. All attributes of one request must belong to one data service, because a token is for one data service.
2. The record's `endpointDescription` is the data service's **contract**. Its server must be the record's `endpointURI`. Its `x-oauth-authorization-server` names the **authorization server's metadata** (RFC 8414), and the issuer must be the prefix of that URL.
3. **Register** (RFC 7591) with a software statement sealed with the QTSP certificate, carrying its `registryURI`. This happens once per authorization server; the web side does it at start.
4. **Authorize**: the authorization code flow with PKCE S256, `state`, the operation as scope, and `resource` = the record's `endpointURI`.
5. **Token**: `private_key_jwt` with the certificate key, and a DPoP proof. The token is DPoP-bound.
6. **Verify or retrieve** with the token and a DPoP proof, in a W3C trace of its own (`traceparent`), which the ASIP's spans join. A 200 is accepted only with a valid **seal**: the detached JWS in `X-JWS-Signature` over the exact body, whose certificate chain leads to `QTSP_SEAL_TRUST_ANCHORS`. Otherwise the result is reported as not accepted.

## Web

`GET /` shows a form: verify or retrieve, and the five BRP attributes of the demo with the values of test person Frouke Jansen as examples. `POST /start` plans, registers if needed, and sends the browser to the authorization server. It keeps `state` and the PKCE verifier in a session behind an HttpOnly, SameSite=Lax cookie (in memory, 10 minutes). `GET /callback` checks the session and `state`, takes the token, asks the ASIP, and shows the result per attribute with the seal check. A denial shows "Geweigerd"; a callback without a matching session or `state` is refused before any token request.

### Request API

The developer portal lays a request down here and follows it, while the user still goes through the QTSP:

- `GET /api/attributes`: the attributes the form offers, with an example value.
- `POST /api/requests` with `{"operation":"verify","attributes":[{"identifier":"…","value":{…}}]}` (for `retrieve`, without values). The QTSP checks the request, finds the data service and registers, as the form does, and answers 201 with an `id` and a `start_url`. A request it does not start gets an RFC 9457 problem with the reason: 400 for an invalid request, 422 when no data service provides the attributes, 502 when discovery or registration fails.
- `start_url` (`/requests/{id}/start`) sends the browser to the authorization server. It carries only the id, so no value appears in a URL, and it works once (410 after that). The user returns to the result page.
- `GET /api/requests/{id}`: `pending` until the user returns, then `done` (the result per attribute, the seal check, and `trace_id`: the trace of the call at the ASIP), `denied` (the OAuth error) or `failed` (an error answer of the ASIP, or no token).

A request and its outcome can be fetched for 10 minutes; then it is gone (404). The API has no access control, like the other mocks of the demo: anyone who can reach the port can lay a request down.

The authorization server allows an http redirect URI on a loopback address only, so the web side works with the browser on the same machine (`http://localhost:9427`).

## CLI

The same flow from the command line. The test harness plays the user at the test-person chooser; a real user does that in a browser.

```bash
qtsp-reference verify --attr 'https://gbo.example/attributes/family_name/1={"family_name":"Jansen"}' --person frouke
qtsp-reference retrieve --attr https://gbo.example/attributes/address/1 --person frouke
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
- `QTSP_URL_MAP`: `public=internal,…`. The records and contracts name public URLs, which a container cannot reach. The map changes the transport only; DPoP proofs and audiences keep the public URLs.

If the authorization server refuses the registration (for example, no `QEAA_Provider` entitlement), the QTSP stops with the reason. While discovery or the server is unavailable, it tries again.

## Layout

| Package | |
|---|---|
| `internal/qtsp` | the flow: discover, register, authorize, token, verify or retrieve, and the seal check |
| `internal/seal` | validation of the seal on a result |
| `internal/harness` | the test harness at the chooser |
| `internal/cli` | the CLI: options, one run, the report |
| `internal/web` | the web side and the request API |
| `internal/urlmap` | the transport map from public to internal URLs |
| `main.go` | composition root |

`e2e_test.go` builds the authorization server, the ASIP and the DIP stub and starts them, with a fake Outway in front of the BRP. Through them it runs every verification result, the error answers 400, 401, 404 and 501, retrieve, a seal from another anchor, a refused registration, a denial, the web side from form to result, and the request API (`api_e2e_test.go`).
