# ldv-client

## Purpose

The one Go module that owns the LDV record wire format and the producer side:
configuration, confirmed write, local spool (`Outbox`), trace context, subject
pseudonyms, FSC claims. `ldvtest` is the fake logbook for producers' tests. Not
an OTel exporter. What a service logs, and about whom, stays in that service.
Not published: consumers use a `replace` to `../ldv-client`
(`grep -l gbo-demo/ldv-client services/*/go.mod`).

## Contracts

- `New` returns `nil, nil` without a logbook URL: the component writes nothing.
  No mode drops records.
- `Write` returns once the record is fsynced to the outbox, or, without one, on
  the logbook's confirmation. Callers fail their request on its error.
- The subject headers (`HeaderSubjectID`) pass a pseudonym between components of
  one Verantwoordelijke only; never across FSC. Nothing enforces this.
- The pseudonym key differs per Verantwoordelijke; a shared key links one
  citizen across logbooks.
- `TraceContextFrom` reads traceparent, then the FSC transaction id, and the
  ambient span last: behind `otelhttp` that span always exists and would shadow
  the headers.
- Extra attributes use the `dpl.gbo.` prefix.
- Wrap as `otelhttp.NewTransport(ldv.Transport(base))`, or callee records hang
  under an OTel span no logbook reader can resolve.

## Verify

`go test ./...` here and in every module that replaces it; then `make demo-dvtp`
and check the chain still assembles. Rigor: **heavy**: eight services and the
logbook share this contract; a fault mislogs or leaks personal data everywhere
at once.

## Decisions

- One module instead of per-service DTOs, wire format per LDV §3.2.2, OTel's
  Trace Context propagator, durable spool instead of fail-closed:
  [#317](https://github.com/ICTU/GBO-demo/pull/317).
- Subject named in each Verantwoordelijke's own pseudonym space:
  [#366](https://github.com/ICTU/GBO-demo/pull/366).

## Open questions

- `Outbox.Deliver` stops at the first failure, so a record the logbook refuses
  for good (`422`, `409`) blocks every later one, with only a log line. How
  should such a record leave the outbox, and who should notice?
- The logbook README says `consent-portal-backend` uses none of this module; its
  `ldv/` adapter and `main.go` use `ldvclient` and `Outbox`. Stale?

## Links

- [../ldv-logboek/README.md](../ldv-logboek/README.md), "Producers" and "The
  client": guarantees and producer variables; open before changing `Write`,
  `Outbox` or configuration.
