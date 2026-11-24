# ldv-client

## Purpose

The one Go module that owns the LDV record wire format and the producer side:
configuration, confirmed write, local spool (`Outbox`), trace context, subject
pseudonyms, FSC claims. `ldvtest` is the fake logbook for producers' tests. What
a service logs, and about whom, stays in that service. Not published: producers
use a `replace` to `../ldv-client`, so a change here rebuilds every producer
image (they build from the repository root).

## Contracts

- The subject headers (`HeaderSubjectID`) pass a pseudonym between components of
  one Verantwoordelijke only; never across FSC. Nothing enforces this.
- Producers wrap as `otelhttp.NewTransport(ldv.Transport(base))`; the other
  order files callee records under an OTel span no logbook reader can resolve.

## Verify

`go test ./...` here and in every module that replaces it
(`grep -l gbo-demo/ldv-client services/*/go.mod`). End to end: `make demo-dvtp`,
run a DvTP query, and check that the chain still assembles across logbooks,
against "What the flows produce" in
[../ldv-logboek/README.md](../ldv-logboek/README.md).

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
