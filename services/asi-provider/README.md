# asi-provider — ASIP mock (ETSI TS 119 478)

A mock Authentic Source Interface Provider. A qualified trust service provider (QTSP) asks whether an attribute value it holds for a person matches the authentic source, and gets one of `Match`, `NoMatch`, `MatchWithVariation` or `Unknown` back. Part of epic #336: slice 1 (#498) built the contract layer, slice 2 (#503) the authorization, slice 4 (#525) the chain to the BRP.

## What is real and what is not

| Part | Status |
|---|---|
| `POST /<data service>/authsrc-api/verify` and `/retrieve` (ETSI TS 119 478 V1.1.1 clause 6.1.1 and 6.1.2) | implemented against the contract below, per data service; the tests validate every response against it |
| Data services | one per authentic source (`config/dataservices.json`); the demo has one, `brp` |
| Sealed results | every successful response is sealed; the certificate is a **development** certificate, not a qualified one |
| Authentic source | the BRP mock, asked over FSC through the ASIP's own Outway; the PDP of the BRP Inway judges each query (rule `ASI0001`) |
| Authorization (clause 6.1.3) | every request needs a DPoP-bound access token from [`asi-authorization-server`](../asi-authorization-server); the BSN of the user comes from the token. User identification at the authorization server is still a stub |
| Attribute identifiers | provisional (`https://gbo.example/attributes/<name>/1`); there is no Dutch catalogue of attributes yet |

## Run

In the demo stack, after `make demo-eudi` or `make demo-full`:

```bash
make demo-asi
```

This adds the ASIP's FSC peer (OIN `99999999900000001200`, compose profile `asi`), lists it in the onboarding register with source peer `…0200`, seeds its connection contract for the BRP source (`brp`) with the grant link `/brp` on its Outway, issues the development seal certificate when missing, and starts both services: the authorization server on `GBO_PORT_ASI_AUTHORIZATION_SERVER` (default 9425) and the ASIP on `GBO_PORT_ASI_PROVIDER` (default 9424). Both are reached from the host: the issuer and the ASIP's public URL are host addresses (`http://<GBO_PUBLIC_HOST>:<port>`, or `ASI_AUTHORIZATION_SERVER_PUBLIC_URL` and `ASI_PROVIDER_PUBLIC_URL`; set `GBO_PUBLIC_HOST` to the LAN address when the QTSP or the browser runs on another machine), and the ASIP fetches the authorization server's keys on its internal address (`ASIP_AUTHORIZATION_SERVER_INTERNAL_URL`).

On its own, it needs an Outway with a contract for `brp`; outside the stack every query then answers 503:

```bash
make provision-asip-seal-certificate
cd services/asi-provider
ASIP_SEAL_KEY_FILE=../../.local/secrets/asi-provider/seal-key.pem \
ASIP_SEAL_CERT_FILE=../../.local/secrets/asi-provider/seal-cert.pem \
ASIP_OUTWAY_URL=http://localhost:8080 \
go run .
```

A request needs an access token from the authorization server, obtained through the authorization code flow; see [`asi-authorization-server`](../asi-authorization-server). There is no client to do that by hand yet (slice 6 of #336); the end-to-end test of the authorization server runs the whole flow against this service.

Configuration: `ASIP_ADDR` (default `:4020`), `ASIP_PUBLIC_URL` (the scheme and host QTSPs use, default `http://localhost:4020`; the token audience of a data service is this URL plus `/<data service>/authsrc-api`), `ASIP_AUTHORIZATION_SERVER` (the issuer, default `http://localhost:4021`), `ASIP_AUTHORIZATION_SERVER_INTERNAL_URL` (where the ASIP reaches the authorization server when that differs from the issuer), `ASIP_OUTWAY_URL` (required: the ASIP's Outway), `ASIP_DATASERVICES_FILE` (default `config/dataservices.json`), `ASIP_SEAL_KEY_FILE`, `ASIP_SEAL_CERT_FILE` (chain, leaf first).

## Data services

ETSI TS 119 478 lets a response name one authentic source (REQ-ASIP-6.1.1.2-08), and the Data Service Directory lists a data service per attribute (REQ-DIP-5.3.3-03). One ASIP for several authentic sources therefore serves a data service per source, each under `/<data service>/authsrc-api` with its own contract (`/<data service>/openapi.json`, the `endpointDescription`), catalogue and token audience.

`config/dataservices.json` lists them. Per data service:

| Field | |
|---|---|
| `id` | the path segment |
| `provider` | the legal name of the ASIP in the responses |
| `authentic_source` | the legal name of the authentic source; empty when the ASIP is the authentic source, and then the responses name none |
| `country` | the Member State of the data service (ISO 3166-1 alpha-2), in its discovery records |
| `source` | the adapter that asks the source; it is code (`brp`: `internal/brpsource`) |
| `grant_path` | the grant-link of the ASIP's FSC contract with the source, on its Outway |
| `attributes` | per attribute identifier, the name the adapter knows it by |

The ASIP does not start with a data service named twice, an unknown adapter, an attribute the adapter cannot ask for, or a field it does not know (a misspelt `authentic_source` would otherwise drop the source from the responses). The authorization server must know the base URL of each data service as a resource (`AZSP_RESOURCES`); a token is for one data service. An attribute of another data service is not in the catalogue: 404. All data services share one seal for now; who holds it is open (#225).

### Discovery records

`GET /<data service>/dataservices` serves the data service's `dataService` records (ETSI TS 119 478 clause 5.3.3, `19478-dataservice-schema.json`), one per attribute: what the ASIP hands to the Data Service Directory. `endpointURI` is the data service's base URL (for verify and retrieve), `endpointDescription` its contract, `provider` the ASIP's legal name and `country` the data service's country. The provider carries no `identifiers` or `establishedByLaw`: there is no EU identifier type for an OIN, and the demo ASIP has no EUID, LEI or legal basis, so the "should" of REQ-DIP-5.3.3-03-04-04 is not met. [`asi-discovery-stub`](../asi-discovery-stub) answers the Discover Interface's `retrieve` query from these records.

## Layout

Ports and adapters, as `AGENTS.md` asks for a service with an authorization boundary:

| Package | |
|---|---|
| `internal/verification` | the core: Verify and Retrieve, the catalogue, matching and JSONPath fragments. Port `Source` |
| `internal/accesstoken` | the ASIP as resource server: access token, DPoP proof and scope. Port `KeySource` |
| `internal/asmetadata` | `KeySource`: the keys from the metadata of the authorization server |
| `internal/brpsource` | `Source`: the BRP over FSC, one GraphQL query per request |
| `internal/dataservice` | the data services file |
| `internal/tracing` | the `Fsc-Transaction-Id` of a request, its trace, and the OTLP exporter |
| `internal/seal` | the seal on every result |
| `internal/sealcert` | the `provision-seal-certificate` command |
| `internal/httpapi` | per data service `/verify`, `/retrieve` and the contract: validation, problem details, `WWW-Authenticate` |
| `openapi` | the contract files, embedded |
| `main.go` | composition root |

## The contract

`GET /<data service>/openapi.json` serves the contract of a data service: the OpenAPI of ETSI TS 119 478 Annex B, unchanged (`openapi/19478-authentic-source-interface-openapi.json`), with the completions in `openapi/completions.json` applied as a JSON Merge Patch (RFC 7386). The completions do not deviate from ETSI; they fill in what Annex B leaves open:

- **Server**: the base URL of the data service, e.g. `http://localhost:4020/brp/authsrc-api`, fills in the Annex B server template; `authsrc-api` is its default base path.
- **OAuth 2.0**: Annex B defines no security scheme; clause 6.1.3 requires an access token from the authorization server. The contract describes the authorization code flow with the URLs of the configured authorization server (`ASIP_AUTHORIZATION_SERVER`), names its RFC 8414 metadata in `x-oauth-authorization-server`, and requires the scope of each operation (REQ-DIP-5.3.3-03-02-01 asks the OpenAPI to specify the authorization server's metadata).
- **`WWW-Authenticate`** on every 401: RFC 6750 section 3 and RFC 9068 section 4 require the challenge.
- **503**: when the keys of the authorization server cannot be obtained, no token can be validated. That is not an error of the client's token, so the mock answers 503 rather than 401. The same when the BRP cannot be reached: that is not `Unknown`, which says the source holds no data. Annex B lists no 503.
- **500**: when the chain refuses the ASIP's query (the PDP denies it, or the BRP answers errors). That is a fault in the deployment, not a missing value. Annex B lists no 500.
- **`X-JWS-Signature`** on every 200: the seal the law requires (below).
- **400 and 501 on `/retrieve`**: REQ-ASIP-6.1.2.2-06 lists them; Annex B lists only 200, 401 and 404.
- **Problem details on 401, and on 404 of `/retrieve`**: Annex B describes no body there; this mock returns the same RFC 9457 problem as on its other errors.

The access token is checked first (401). Then the request is validated against the request schemas of the contract, so a malformed request gets 400 before an unsupported mandate (501) or an unknown attribute (404). An attribute identifier must be an absolute URI.

## The authentic source

The ASIP is an FSC consumer peer of its own (`99999999900000001200`) with a connection contract for the BRP service `brp`. Per request it posts one GraphQL query of its own to the grant-link `/brp/graphql` on its Outway, with only the fields of the requested attributes and the BSN from the access token as the variable `bsn`: one FSC transaction and one PDP decision per request. If the chain refuses a field, the whole request fails (500): in this chain that is a fault in the deployment. ETSI TS 119 478 does not prescribe how an ASIP asks its source. Every request gets one `Fsc-Transaction-Id` (UUID v7) for its query. Without a `traceparent` from the QTSP, the trace id is that UUID, so the FSC transaction log, the PDP's decision log and the trace share one value; with one, the request joins the QTSP's trace. The ASIP exports spans over OTLP when `OTEL_EXPORTER_OTLP_ENDPOINT` is set (`OTEL_SERVICE_NAME`, default `asi-provider`): the request, with the data service as `gbo.asi.data_service`, and the BRP query, with `gbo.fsc.transaction_id` and the attribute names as `gbo.asi.attributes`. No span holds the BSN or a value. The developer portal's ASI tab watches a verify or retrieve at the ASIP (the QTSP starts it; the portal does not yet), and shows the chain, the transaction log of the ASIP's peer (`asip`) and BD, and the PDP decision, all found from that transaction id.

| Attribute | BRP fields | Value |
|---|---|---|
| `family_name` | `voorvoegsel`, `geslachtsnaam` | `{"family_name": "<voorvoegsel> <geslachtsnaam>"}` |
| `given_name` | `voornamen` | `{"given_name": …}` |
| `birth_date` | `geboortedatum` | `{"birth_date": …}` |
| `nationality` | `heeftNationaliteit { nationaliteit }` | `{"nationalities": [ISO codes]}` |
| `address` | `... on Ingezetene { woontOp { … } }` | `{"street", "house_number" (number, letter and addition separated by spaces), "postal_code", "city", "country": "NL"}` |

A person the BRP does not know, or a field it holds no value for, yields `Unknown`; an address yields it unless street, house number, postal code and city are all there. So does the address of a non-resident: a foreign address in the BRP has free lines, not the members of the attribute.

Rule `ASI0001` (`policies/gbo/rules/asi/asi0001.rego`) grants these fields to the ASIP's peer, without a consent token, for a BSN that names a citizen, on the service `brp` only. Any other BRP field falls to the closed world of the policy engine.

## The access token

Every request carries `Authorization: DPoP <token>` and a `DPoP` proof header. The ASIP validates, as RFC 9068 section 4 and RFC 9449 sections 4.3 and 7 require:

- `typ` is `at+jwt`; the signature verifies with a key from the `jwks_uri` in the metadata of the authorization server (ES256, or RS256 as RFC 9068 section 2.1 requires); `iss` is the authorization server; `aud` is this ASIP; `exp` has not passed (30 seconds leeway);
- the token is DPoP-bound (`cnf.jkt`) and carries the BSN (`https://gbo.example/claims/bsn`);
- exactly one proof, `typ` `dpop+jwt`, signed with its public `jwk`, for this method and URL, for this token (`ath`), at most 60 seconds from now, used once, and its key is the key the token is bound to;
- the scope contains the operation: `verify` for `/verify`, `retrieve` for `/retrieve`.

Every failure is 401 with a `WWW-Authenticate: DPoP` challenge and a problem body: `invalid_token`, `invalid_request` (no proof), `invalid_dpop_proof`, or `insufficient_scope`. ETSI TS 119 478 lists 401 for "an authentication or authorization error"; RFC 6750 would use 403 for `insufficient_scope`.

A token without the BSN claim is `invalid_token`. A BSN the BRP does not know yields `Unknown`. When the ASIP cannot obtain the keys of the authorization server and holds none from before, it answers 503.

`GET /19478-dataservice-schema.json` serves the schema the contract references.

## The seal

Implementing Regulation (EU) 2025/1569 Article 9(4), as amended by Implementing Regulation (EU) 2026/1735, requires every verification result to be signed or sealed by the responsible public sector body or the designated intermediary, from 1 January 2027. ETSI TS 119 478 clause 6.1.1 does not define how.

This mock seals the exact response body with a detached JWS (RFC 7515, appendix F) in the `X-JWS-Signature` header: `<protected header>..<signature>`, algorithm ES256, the certificate chain in `x5c` (leaf first). The body stays plain JSON as the OpenAPI specifies. A client validates the signature over the received body and the chain against its trust anchors.

`make provision-asip-seal-certificate` issues the seal certificate from the development issuer CA in `$(DEVELOPMENT_CA_DIR)`. In production this must be a qualified certificate for electronic seals (Implementing Regulation (EU) 2025/1943, Annex II), with the organisation's registration number as `organizationIdentifier`; who holds it is open (#225).

## Interpretations

Where ETSI TS 119 478 leaves room, this mock chooses as follows (D10 in #336):

- **`Unknown`** means "no authentic source data was available to determine a match for the attribute for the user" (REQ-ASIP-6.1.1.2-04): the source holds no value for this person. The ARF (QTSPAS_02) answers `NoMatch` in the second case; ETSI clause 6.1.1 is the legally designated text, so it prevails.
- **404** is returned only for an attribute identifier that is not in the catalogue of the data service, also when it is another data service's.
- **No value is returned.** `Match` and `MatchWithVariation` carry no `attributeValue`, although Implementing Regulation (EU) 2026/1735 Annex IV allows it.
- **`MatchWithVariation`** is returned when the values are equal after transliterating Latin letters that do not decompose as in ICAO Doc 9303 (ß → ss, æ → ae, ø → oe, ĳ → ij, …), removing diacritics, treating hyphens as spaces, collapsing white space and folding case. Transliteration between scripts (Cyrillic, Greek, …) is not supported.
- **Arrays** (such as nationalities) are compared as sets: order does not matter.
- **Fragments** support the JSONPath subset `$`, `.name`, `['name']` and `[n]`. The schema requires a fragment `value` to be an object; this mock reads it as `{"<last member name>": value}`, for example location `$.city` with value `{"city": "Rotterdam"}`.
- **`mandate`** is not supported and returns 501, also on `/retrieve`.
- **Processing** is synchronous only.
- **Errors** are `application/problem+json` (RFC 9457) and are not sealed.
