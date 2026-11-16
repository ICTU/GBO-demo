# dienstverlener-backend

The server-side half of a demo service provider. Compose runs it twice:
as `dienstverlener-backend` for Hypotheek-BV (peer `…0300`, `QUERY_KIND=bd`)
and as `ir-backend` for the Installatie Register (peer `…1000`,
`QUERY_KIND=lvg`). Its frontend (`dienstverlener-mock`, `keyper-mock`) reaches
it only through the Vite proxy at `/api`; the backend reaches everything else
through its own FSC Outway.

## Endpoints

| Route | What it does |
|---|---|
| `POST /api/consent/start` | Pushes a consent request to the consent portal and answers with the URL the browser goes to |
| `POST /api/consent/complete` | Takes what the browser brought back from the portal and, for a code, exchanges it for the consent token |
| `POST /api/dvtp/query` | Asks the source under a consent: the token in the body, or else the one in the session |
| `GET /health` | Health check |

### Asking for consent

`POST /api/consent/start` with `{"service": "…", "scopes": ["…"], "valid_until": "…"}`
(`valid_until` optional) pushes a request (RFC 9126) to `POST /par` of the
consent portal's FSC service `consent-authorization`, through the Outway. The
request carries `response_type=code`, `client_id` (`CONSENT_CLIENT_ID`, the
provider's own Peer ID), `service`, `scope`, `redirect_uri`
(`CONSENT_REDIRECT_URI`), a random `state` and an S256 PKCE `code_challenge`.
It sends no `client_name` or `purpose`: the consent screen shows those from
the provider's registration. The answer is `{"redirect_to": "<portal>/toestemming/start?client_id=…&request_uri=…"}`,
and the response sets the session cookie. A refusal by the portal keeps its
OAuth `error` and `reason` (`400`, or `503` for `temporarily_unavailable`); an
unreachable portal is `502`.

`POST /api/consent/complete` with `{"code": "…", "state": "…"}` or
`{"error": "…", "state": "…"}` ends the request pending in the session. The
state must match; otherwise `400 state_mismatch` and the request stays
pending. A code is exchanged at `POST /token` with the PKCE verifier; the
consent token goes into the session. The answer is
`{"outcome": "granted", "consent_id": "…", "scopes": [...]}`,
`{"outcome": "denied"}` (`access_denied`) or `{"outcome": "failed"}`
(`server_error` or any other error), each with `200`. A failed exchange is
`502 code_exchange_failed`. No answer carries the consent token.

The session is an HttpOnly cookie `consent_session_<CONSENT_CLIENT_ID>` with
`SameSite=Lax` and `Path=/api`, `Secure` when `CONSENT_REDIRECT_URI` is https.
The name includes the peer because a browser does not tell cookies of two
ports on one host apart. Calls to the Outway carry the trace context
(`traceparent`).

Demo shortcut: sessions, with their consent tokens, live in memory for 12
hours; a restart drops them and the backend runs as one replica. A real
deployment keeps sessions in a shared store.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `4006` | Listen port |
| `QUERY_KIND` | `bd` | Which question the source gets: `bd` or `lvg` |
| `OUTWAY_URL`, `OUTWAY_PATH` | `http://hv-outway:8080`, `/bri/graphql` | The source, through the Outway |
| `CONSENT_CLIENT_ID` | unset | The provider's Peer ID; unset turns the consent routes off |
| `CONSENT_REDIRECT_URI` | | A return location registered for the peer in the onboarding register (exact match) |
| `CONSENT_PORTAL_URL` | | Public address of the consent portal |
| `CONSENT_AUTHORIZATION_URL` | | `consent-authorization` through the Outway, e.g. `http://hv-outway:8080/consent-authorization` |
| `LDV_LOGBOOK_URL`, `LDV_WRITE_TOKEN`, `LDV_NEXT_LOGBOOK_IDS` | | The provider's own logbook |
| `DEV_PORTAL_BACKEND_URL` | | Dev-portal timeline (best effort) |

With `CONSENT_CLIENT_ID` set, the other three `CONSENT_*` variables must be
absolute URLs or the service does not start.

## Tests

```sh
go vet ./... && go test -race ./...
golangci-lint run --config ../../.golangci.yml ./...
```
