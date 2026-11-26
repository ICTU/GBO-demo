# bron-sidecar

## Purpose

The source-owned gateway between a source's Inway and its GraphQL server. One
image, three instances: `bron-sidecar` (BD), `brp-sidecar` and `lvg-sidecar`,
told apart by `UPSTREAM_URL`, `OWN_PEER_OIN`, `PSEUDONYM_VARS` and their LDV
settings. Under a consent token (DvTP) it puts the BSN or the source's own
pseudonym in place of the placeholder `consent:identity` or `consent:pseudonym`,
read from the token's value for `OWN_PEER_OIN` by the decryption component
(`DECRYPTION_URL`) with the keys in `SUBJECT_KEYS_DIR`. Without a token (EUDI,
ASI) it passes the request on. It decides nothing: the PDP did, before the Inway
forwarded.

## Contracts

- No authentication, and the consent token is not verified here: only the Inways
  may reach it (network `source-gateways`, no published port). A caller that
  reaches it directly skips the PDP.
- It forwards only the FTV GraphQL profile's transport subset (`transport.go`).
  The `Content-Type` check is the PDP mapper's own `ftvgraphql.IsJSONMediaType`,
  through a `replace` to `../ftv-graphql-mapper`, so a change there rebuilds
  this image.
- Under a token, a subject variable with a literal value is refused, and so is a
  value made for another OIN. `OWN_PEER_OIN` must be the OIN that the consent
  portal's `CONSENT_SOURCES` names and the key files are for
  (`config/demo-keys/README.md`). Without `SUBJECT_KEYS_DIR` every request under
  a token fails.
- The placeholders are shared with `dienstverlener-backend`
  (`consumer/token.go`), `policies/gbo/lib.rego` and the developer portal;
  rename them everywhere at once.
- LDV: two records per request with a subject (decryption, forward), spooled in
  the outbox; the source gets the sidecar's subject reference in
  `ldv.HeaderSubjectID`, so both name the Betrokkene alike. The LDV client is
  nil without `LDV_LOGBOOK_URL`: every use needs a nil guard. Records assume one
  subject per request (`logForward`).
- Deliveries: `DATA_USAGE_INDEX_URL`, `DATA_USAGE_LOGBOOK_ID`,
  `DELIVERY_LOGBOOK_URL` and `DELIVERY_ACTIVITY` all or none. After a `200`
  under a token it registers the trace at the index, then writes the delivery
  record directly (no outbox); if either fails the data is withheld.
  `DATA_USAGE_LOGBOOK_ID` must match the consent register's keys (see
  [../data-usage-index](../data-usage-index/AGENTS.md)).

## Pitfalls

- A missing nil guard on the LDV client made a sidecar without a logbook panic
  on every FSC request: [#317](https://github.com/ICTU/GBO-demo/pull/317).
- Go's media-type parser accepted `Content-Type` forms the mapper refuses, so
  the source would have run requests the PDP never judged:
  [#494](https://github.com/ICTU/GBO-demo/pull/494).

## Verify

`go test ./...` here, and in `../ftv-graphql-mapper` after a transport change.
End to end: `make demo-dvtp`, run a DvTP query, then read the chain
(`curl -s http://localhost:9407/ldv/<trace-id> | jq`) and the consent's uses
(`curl -s http://localhost:9402/consents/<consent-id>/uses | jq`).

## Decisions

- The subject is filled in at the source after the allow, from values per party
  in the token, without calling BSNk:
  [#469](https://github.com/ICTU/GBO-demo/pull/469); two placeholders, identity
  and pseudonym: [#476](https://github.com/ICTU/GBO-demo/pull/476),
  [#477](https://github.com/ICTU/GBO-demo/pull/477).
- Only the Inways reach the sidecars:
  [#469](https://github.com/ICTU/GBO-demo/pull/469).
- The profile's transport subset enforced at the source:
  [#494](https://github.com/ICTU/GBO-demo/pull/494).
- Each delivery under a consent registered and recorded before the data leaves:
  [#706](https://github.com/ICTU/GBO-demo/pull/706).

## Open questions

- `brp-sidecar` registers deliveries as peer `…0400`, where `fsc-seed-brp`
  publishes service `brp`, while the EUDI issuance and the ASIP use `brp` on
  `…0200`, and `docker-compose.yml` still calls it pass-through in practice. Is
  `…0400` the BRP source for DvTP, and is that comment stale?

## Links

- [../../docs/consent-flow.md](../../docs/consent-flow.md) (Dutch): the DvTP
  flow this sidecar ends; open before changing substitution or deliveries.
