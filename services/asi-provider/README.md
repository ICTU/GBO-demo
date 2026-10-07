# asi-provider — ASIP mock (ETSI TS 119 478)

A mock Authentic Source Interface Provider. A qualified trust service provider (QTSP) asks whether an attribute value it holds for a person matches the authentic source, and gets one of `Match`, `NoMatch`, `MatchWithVariation` or `Unknown` back. Part of epic #336; this is slice 1 (#498).

## What is real and what is not

| Part | Status |
|---|---|
| `POST /authsrc-api/verify` and `POST /authsrc-api/retrieve` (ETSI TS 119 478 V1.1.1 clause 6.1.1 and 6.1.2) | implemented against the contract below; the tests validate every response against it |
| Sealed results | every successful response is sealed; the certificate is a **development** certificate, not a qualified one |
| Authentic source | in-memory, `testdata/persons.json` (the persons of the BRP mock) |
| Authorization server (clause 6.1.3) | **stub**: fixed test tokens identify test persons. Not conformant; slice 2 replaces it |
| Attribute identifiers | provisional (`https://gbo.example/attributes/<name>/1`); there is no Dutch catalogue of attributes yet |

## Run

```bash
make provision-asip-seal-certificate
cd services/asi-provider
ASIP_SEAL_KEY_FILE=../../.local/secrets/asi-provider/seal-key.pem \
ASIP_SEAL_CERT_FILE=../../.local/secrets/asi-provider/seal-cert.pem \
go run .
```

```bash
curl -s -X POST localhost:4020/authsrc-api/verify \
  -H 'Authorization: Bearer test-token-frouke' -H 'Content-Type: application/json' \
  -d '{"attributes":[{"attributeIdentifier":"https://gbo.example/attributes/family_name/1","attributeValue":{"family_name":"Jansen"}}]}'
```

Configuration: `ASIP_ADDR` (default `:4020`), `ASIP_SOURCE_FILE`, `ASIP_SEAL_KEY_FILE`, `ASIP_SEAL_CERT_FILE` (chain, leaf first), `ASIP_STUB_TOKENS` (`token:bsn,…`; defaults to `test-token-frouke`, `test-token-joost`, `test-token-sanne`, `test-token-tom`), `ASIP_PROVIDER_NAME`, `ASIP_AUTHENTIC_SOURCE_NAME`.

## The contract

`GET /openapi.json` serves the contract: the OpenAPI of ETSI TS 119 478 Annex B, unchanged (`openapi/19478-authentic-source-interface-openapi.json`), with the completions in `openapi/completions.json` applied as a JSON Merge Patch (RFC 7386). The completions do not deviate from ETSI; they fill in what Annex B leaves open:

- **Server**: `http://localhost:4020/authsrc-api` fills in the Annex B server template; `authsrc-api` is its default base path.
- **Bearer token**: Annex B defines no security scheme; clause 6.1.3 requires an access token from the authorization server.
- **`X-JWS-Signature`** on every 200: the seal the law requires (below).
- **400 and 501 on `/retrieve`**: REQ-ASIP-6.1.2.2-06 lists them; Annex B lists only 200, 401 and 404.
- **Problem details on 401, and on 404 of `/retrieve`**: Annex B describes no body there; this mock returns the same RFC 9457 problem as on its other errors.

Every request is validated against the request schemas of the contract before anything else is looked at, so a malformed request gets 400 before an unknown attribute (404) or an unsupported mandate (501). An attribute identifier must be an absolute URI.

`GET /19478-dataservice-schema.json` serves the schema the contract references.

## The seal

Implementing Regulation (EU) 2025/1569 Article 9(4), as amended by Implementing Regulation (EU) 2026/1735, requires every verification result to be signed or sealed by the responsible public sector body or the designated intermediary, from 1 January 2027. ETSI TS 119 478 clause 6.1.1 does not define how.

This mock seals the exact response body with a detached JWS (RFC 7515, appendix F) in the `X-JWS-Signature` header: `<protected header>..<signature>`, algorithm ES256, the certificate chain in `x5c` (leaf first). The body stays plain JSON as the OpenAPI specifies. A client validates the signature over the received body and the chain against its trust anchors.

`make provision-asip-seal-certificate` issues the seal certificate from the development issuer CA in `$(DEVELOPMENT_CA_DIR)`. In production this must be a qualified certificate for electronic seals (Implementing Regulation (EU) 2025/1943, Annex II), with the organisation's registration number as `organizationIdentifier`; who holds it is open (#225).

## Interpretations

Where ETSI TS 119 478 leaves room, this mock chooses as follows (D10 in #336):

- **`Unknown`** means "no authentic source data was available to determine a match for the attribute for the user" (REQ-ASIP-6.1.1.2-04). That covers both an attribute this source is not the authentic source for and a person for whom the source holds no value. The ARF (QTSPAS_02) answers `NoMatch` in the second case; ETSI clause 6.1.1 is the legally designated text, so it prevails.
- **404** is returned only for an attribute identifier that is not in the catalogue.
- **No value is returned.** `Match` and `MatchWithVariation` carry no `attributeValue`, although Implementing Regulation (EU) 2026/1735 Annex IV allows it.
- **`MatchWithVariation`** is returned when the values are equal after transliterating Latin letters that do not decompose as in ICAO Doc 9303 (ß → ss, æ → ae, ø → oe, ĳ → ij, …), removing diacritics, treating hyphens as spaces, collapsing white space and folding case. Transliteration between scripts (Cyrillic, Greek, …) is not supported.
- **Arrays** (such as nationalities) are compared as sets: order does not matter.
- **Fragments** support the JSONPath subset `$`, `.name`, `['name']` and `[n]`. The schema requires a fragment `value` to be an object; this mock reads it as `{"<last member name>": value}`, for example location `$.city` with value `{"city": "Rotterdam"}`.
- **`mandate`** is not supported and returns 501, also on `/retrieve`.
- **Processing** is synchronous only.
- **Errors** are `application/problem+json` (RFC 9457) and are not sealed.
