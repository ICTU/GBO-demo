# ldv-client

## Contracts

- The subject headers (`HeaderSubjectID`) pass a pseudonym between components of
  one Verantwoordelijke only; never across FSC. Nothing enforces this.

## Verify

`go test ./...` here and in every module that replaces it
(`grep -l gbo-demo/ldv-client services/*/go.mod`).

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
