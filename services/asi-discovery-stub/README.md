# asi-discovery-stub — Discover Interface Provider stub (ETSI TS 119 478)

A stub of the Discover Interface Provider (DIP) of ETSI TS 119 478 clause 5. The DIP is an external role, the Data Service Directory of the common services. The demo stubs it so a QTSP can find the ASIP's data service for an attribute. Part of epic #336, slice 6 (#533).

## What is real and what is not

| Part | Status |
|---|---|
| `GET /retrieve?queryType=dataServices&attributeIdentifier=…[&country=…][&conformsTo=…]` (clause 5.3) | implemented against the Annex A OpenAPI; the tests validate every response against it |
| `GET /search` (clause 5.2, the semantic repository) | not offered: 501 |
| Records | none of its own; per query it reads the `dataService` records the configured ASIP data services publish (`/<data service>/dataservices`) |

## Behaviour

- **Matching:** a record matches on `attributeIdentifier`. With `country`, only records of that country match. With `conformsTo`, only `urn:ietf:rfc:9110` (the HTTP interface of clause 6.1) matches, because the demo's records are all HTTP-based.
- **No match:** an attribute without a record gives 200 with an empty `dataServices` list. Annex A has only 200 and a default (problem) response.
- **Malformed queries:** a query that does not follow Annex A (`queryType` other than `dataServices`, `attributeIdentifier` missing or not an absolute URI) gives 400, as a problem.
- **Unreadable sources:** a source that cannot be read (unreachable, an error status, not JSON) is left out and logged. The other sources still answer.

`GET /openapi.json` serves the Annex A OpenAPI, unchanged, and the two schemas it references are served next to it (`openapi/`, BSD-3, see `openapi/NOTICE.md`).

## Run

`make demo-asi` starts it on `GBO_PORT_ASI_DISCOVERY` (default 9426), with the ASIP's `brp` data service as its source.

On its own:

```bash
cd services/asi-discovery-stub
DIP_SOURCES=http://localhost:4020/brp/dataservices go run .
```

Configuration: `DIP_ADDR` (default `:4022`), `DIP_SOURCES` (required: the URLs of the data services' records, separated by commas).
