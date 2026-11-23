# data-usage-index

## Purpose

GBO's list of pointers "under legal basis X, logbook Y recorded a delivery under
trace Z". It holds no personal data and does not check the consent; the source's
PDP decided that before the delivery. The README covers the two FSC services,
the registration shape and status codes.

## Contracts

- Both listeners trust `Fsc-Authorization` without checking its signature
  (`CallingPeer`); only `cr-inway` may reach them in a real deployment.
- Registration is accepted only when the calling peer is the peer in
  `logbook_id` (`usage.Index.Register`). Keep that check in the core.
- Registration and lookup stay on separate listeners: an Inway forwards every
  path under a service, so one listener would let any registering source read
  every consent's uses.
- The name part of `logbook_id` is, in practice, the source's FSC read service
  (`ldv-lezen-bd`). The sidecar's `DATA_USAGE_LOGBOOK_ID` must match a key in
  the consent register's `CONSENT_USAGE_READ_SERVICES` and
  `CONSENT_USAGE_SOURCES`; an unmatched registration shows no use, silently.
- Every delivery under a consent waits on this service: `bron-sidecar` withholds
  the data when registering or recording the delivery fails.

## Dependencies

Called by the `bron-sidecar` image (`bron-`, `brp-`, `lvg-sidecar`) through its
peer's Outway, and by `consent-register` through `cr-outway`. Calls nothing.
SQLite on its own volume. Published by `make fsc-seed-data-usage-index`
(`fsc-infra/scripts/seed-data-usage-index.sh`).

## Verify

`go test ./...` here. End to end: `make demo-dvtp`, run a DvTP query, then check
the consent's uses in the consent register. Rigor: **normal**; **heavy** for
changes to `CallingPeer`, the peer check or the listener split, which are the
trust boundary.

## Decisions

- Two FSC services, logbook ID `<peer ID>/<read service name>`, no data subject
  fields: [#704](https://github.com/ICTU/GBO-demo/pull/704).

## Open questions

- The README and `bron-sidecar/usageindex` show `logbook_id` as `…0200/bd`;
  compose and [#704](https://github.com/ICTU/GBO-demo/pull/704) use the read
  service name (`…0200/ldv-lezen-bd`). Which is the contract?

## Links

- [README.md](README.md): API, registration shape, what is left out; open before
  changing either listener.
