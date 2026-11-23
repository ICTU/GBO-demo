# data-usage-index

## Contracts

- The name part of `logbook_id` is, in practice, the source's FSC read service
  (`ldv-lezen-bd`). The sidecar's `DATA_USAGE_LOGBOOK_ID` must match a key in
  the consent register's `CONSENT_USAGE_READ_SERVICES` and
  `CONSENT_USAGE_SOURCES`; an unmatched registration shows no use, silently.
- Every delivery under a consent waits on this service: `bron-sidecar` withholds
  the data when registering or recording the delivery fails.

## Dependencies

Called by the `bron-sidecar` image (`bron-`, `brp-`, `lvg-sidecar`) and by
`consent-register`. Its FSC services are published by
`make fsc-seed-data-usage-index`.

## Verify

`go test ./...` here; end to end: `make demo-dvtp`, run a DvTP query, then check
the consent's uses in the consent register.

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
