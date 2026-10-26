# The ASI route: what is real, stubbed or interpreted

The Authentic Source Interface (ASI) route lets a qualified trust service provider (QTSP) verify or retrieve attributes of a citizen at an authentic source, according to ETSI TS 119 478 V1.1.1 clause 6.1 (Article 45e of eIDAS, Implementing Regulation (EU) 2025/1569 Article 9). Epic #336 built it slice by slice. This page states, for each part, what it is:

- **real**: implemented according to the specification, and tested;
- **stub**: stands in for something this project does not have; not conformant;
- **GBO interpretation**: the specification leaves room or is silent, and the demo makes a choice;
- **demo shortcut**: a simplification of the demo that a production deployment does not take over.

The conformance claim per requirement is in [`conformance/`](../conformance/README.md), the weighing of the security considerations in [`conformance/security-considerations.md`](../conformance/security-considerations.md), and the details in the README of each service.

## Roles and services

| Role (ETSI TS 119 478) | Service | |
|---|---|---|
| ASIP, the Authentic Source Interface Provider | [`asi-provider`](../services/asi-provider/README.md) | `verify` and `retrieve`, per data service |
| AZSP, the authorization server | [`asi-authorization-server`](../services/asi-authorization-server/README.md) | registration, identification and approval by the citizen, access tokens |
| Wallet verifier (part of the AZSP's identification) | [`asi-wallet-verifier`](../services/asi-wallet-verifier/README.md) | nl-wallet's `verification_server` |
| DIP, the Discover Interface Provider | [`asi-discovery-stub`](../services/asi-discovery-stub/README.md) | an external role, stubbed |
| TSP, the QTSP that asks | [`qtsp-reference`](../services/qtsp-reference/README.md) | the reference client; not a real QTSP |

Behind the ASIP the request goes through the demo's generic chain: the ASIP's own FSC Outway, the source's Inway with the PDP (rule `ASI0001`), and the BRP mock.

## ASIP

| Part | Status |
|---|---|
| `verify` and `retrieve` against the ETSI OpenAPI, per data service | **real** |
| Results `Match`, `NoMatch`, `MatchWithVariation`, `Unknown`; errors 400, 401, 404, 501 as `application/problem+json` | **real** |
| Attribute fragments (JSONPath) | **real** |
| Fuzzy matching | **GBO interpretation**: limited to the demo's documented normalisation rules |
| `mandate` | **GBO interpretation**: answered with 501 |
| 200 for every processed request, also when all results are `NoMatch`; 404 only for unknown attribute identifiers | **GBO interpretation** |
| Synchronous processing only | **GBO interpretation** |
| No value returned with `MatchWithVariation` | **GBO interpretation**: Annex IV of Implementing Regulation (EU) 2026/1735 makes the value optional, but the requirement of clause 6.1.1.1 on fuzzy matching still asks for it; the conformance matrix records it as failing |
| Seal on every successful result: detached JWS over the body, chain in `x5c` | **GBO interpretation** of the format, which ETSI does not define; the certificate is a **stub** (development CA, not a qualified seal certificate) |
| One data service per authentic source; one authentic source named in a response | **GBO interpretation** |
| Attribute identifiers and their JSON schemas | **stub**: a provisional GBO namespace, to be replaced by catalogue URIs |
| The authentic source: the BRP mock over FSC, judged by the PDP per field | **real** chain, **stub** source data (test persons) |
| One query to the source per request, only for the requested attributes | **real** |
| No BSN in logs, traces or responses; nothing persisted | **real** |
| Access token checks: signature, issuer, audience, expiry, DPoP, scope; attributes when the token names them | **real** |
| RS256 accepted for access tokens | **GBO interpretation**: RFC 9068 requires it; the authorization server does not use it |
| 401 for `insufficient_scope` | **GBO interpretation**: ETSI lists 401 for every authorization error; RFC 6750 uses 403 |

## Authorization server

| Part | Status |
|---|---|
| Authorization code flow with PKCE S256, `private_key_jwt`, DPoP-bound JWT access tokens (RFC 9068), metadata (RFC 8414) | **real** |
| Dynamic client registration with a software statement sealed with the QTSP certificate | **real** |
| The register of wallet-relying parties and its entitlement | **stub**: a file; the national register and its API are not available to this project |
| QTSP certificates | **stub**: test certificates from the development CA, not access certificates according to ETSI TS 119 411-8 |
| Periodic re-verification of the entitlement; a client stays active while the register cannot be consulted | **real** check; **GBO interpretation** of the behaviour when the register is unavailable |
| Registered clients kept in memory | **demo shortcut**: a QTSP registers again after a restart |
| `resource` (RFC 8707) required, one data service per token | **GBO interpretation** |
| Scopes `verify` and `retrieve` | **real** |
| Binding of the token to the selected attributes with RFC 9396 `authorization_details`, optional, and required with `AZSP_REQUIRE_AUTHORIZATION_DETAILS=true` | **GBO interpretation**: an extension with a provisional type; ETSI binds per operation only. Whether a member state imposes it is a policy choice under Article 9(5) of Implementing Regulation (EU) 2025/1569 |
| One request page: the QTSP by its legal name in the register, the operation, the source, the attributes when supplied; log in or deny | **real**; the wording is the demo's own |
| Login with the NL Wallet (OpenID4VP PID disclosure), cross-device and on the same device | **real**, through nl-wallet's verifier. Run with the real wallet cross-device; on the same device only in tests until the route runs over HTTPS in the simulation environment |
| Login with DigiD (mock) | **stub**: checks nothing and provides the BSN only; not real DigiD |
| The identification data in the token: with the NL Wallet the BSN, names, birth date and nationalities; with DigiD (mock) the BSN only | **GBO interpretation** of what the token carries. The NL PID has no place of birth, one of the mandatory PID attributes |
| The BSN in plain text in the token | **demo shortcut**: the QTSP can read it; slice 9 protects it |
| Pages in a government look (coat of arms, Rijksblauw), without script | **real**; no name of an organisation |
| No PAR, no mTLS, no refresh tokens | **GBO interpretation**: one mechanism of each pair ETSI offers; see the security considerations |
| No `iss` in the authorization response (RFC 9207); issued tokens remain valid after an entitlement is withdrawn | open, see the security considerations |

## Wallet verifier

| Part | Status |
|---|---|
| nl-wallet's `verification_server`, built from the vendor checkout | **real** runtime |
| Reader certificates from the development reader CA | **stub**: not registered access certificates |
| One verifier for the authorization server and the reference QTSP | **demo shortcut**: in production each relying party has its own verifier and keys |

## Discover Interface Provider

| Part | Status |
|---|---|
| `retrieve` with `queryType=dataServices` (clause 5.3) against the Annex A OpenAPI | **stub** of an external role, behaving as the specification describes |
| `search` (clause 5.2) | not offered: 501 |
| `dataService` records, one per attribute and data service, published by the ASIP | **real** |
| The authorization server's metadata in the data service's contract (`x-oauth-authorization-server`) | **GBO interpretation**: ETSI requires the reference but defines no field for it |

## Reference QTSP

| Part | Status |
|---|---|
| Discover, register, authorize, token, verify or retrieve, seal check | **real** client behaviour, used as the consistency test of records, contract and metadata |
| The citizen logs in at the QTSP first, with the NL Wallet (name, birth date, nationalities, no BSN) or with test data | **real** with the NL Wallet; the test data are a **stub** |
| A test statement to the NL Wallet after an accepted result, only to the citizen who logged in | **stub** of a qualified attestation: clearly marked as a test, not qualified |
| Login and pending statements kept in memory | **demo shortcut** |

It is not a real QTSP and holds no qualified status.

## Not in this route

- Clause 6.2 (ISO 15000, eDelivery).
- A production register, access certificates, real DigiD, or production key management, including the qualified seal certificate.
- Protection of the BSN in the token (slice 9 of #336).
- A deployment of the route in the simulation environment; it runs in the local stack (`make demo-asi`).
