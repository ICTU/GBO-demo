# Observability

The local Compose environment and the simulation deployment use the same
three observability data paths:

1. Application traces are sent over OTLP to a gateway collector.
2. The collector stores traces in Jaeger and also forwards them to
   `dev-portal-backend/v1/traces` for the live architecture strip.
3. Container logs are shipped to Loki. The developer portal reads the policy
   engine's console decision log from Loki as detail next to the
   Authorization Decision Log, while Grafana provides the broader log view.

The simulation platform currently has no stable, tenant-consumable core OTLP
or Loki endpoint in its tenant contract. The fallback is therefore deployed
once alongside the developer portal in MinEZK and shared with MinBZK, rather
than duplicated per tenant.

OpenFSC does not currently propagate the OpenTelemetry trace context across
the Inway boundary. The applications therefore also record
`gbo.fsc.transaction_id`. The developer portal uses this value to correlate
the consumer trace, the PDP decision and the FSC transaction logs.

The eudi-adapter records the activated `gbo.source_oin` and `gbo.type_id` on
its request span. These values come from the onboarded source record, not from
an adapter catalog. The bron services themselves are recognised by their
`OTEL_SERVICE_NAME` (`bron-sidecar`/`graphql-server` versus
`brp-sidecar`/`brp-graphql-server`).

## Runtime configuration

The production developer-portal image accepts these runtime variables:

| Variable | Purpose |
| --- | --- |
| `JAEGER_PUBLIC_URL` | Public Jaeger UI used by trace links |
| `GRAFANA_PUBLIC_URL` | Public Grafana UI used by log links |
| `EUDI_PUBLIC_URL` | Public issuance-server URL |

`dev-portal-backend` accepts:

| Variable | Purpose |
| --- | --- |
| `ADL_DATABASE_URL` | Authorization Decision Log, read with a SELECT-only role |
| `LOKI_URL` | Loki base URL |
| `LOKI_DECISION_QUERY` | LogQL selector for the engine's console decision log |
| `FSC_TXLOG_*_URL/CERT/KEY/CA` | Local txlog-api or remote Manager logging source |

`BD_HV` and `BD_EDI` are optional provider-log sources. They allow the same
provider Manager to be queried from the authorized perspective of the DvTP
and EUDI consumer peers respectively.

## Data handling

The OpenFTV PDP has no OpenTelemetry instrumentation of its own. It writes
what it evaluates to two places: the request to the Authorization Decision
Log, and the mapped input to the embedded OPA's console decision log. Neither
masks anything. OpenFTV's ADL has no masking option, and the console log
ignores `data.system.log.mask`. A BSN in the request therefore reaches both;
how to keep it out is still open.

Jaeger and Loki are debugging stores, not audit stores. The simulation
deployment uses seven-day retention. OpenFSC transaction logs remain the
authoritative per-hop message metadata, and the Authorization Decision Log the
authoritative record of each authorization decision.

## Not observability: the Authorization Decision Log

The OpenFTV PDP writes one record per evaluation to the Authorization Decision
Log (Logius ADL), in the `ftv_adl` database on `postgres-ftv`. That record is
the authoritative audit record of an authorization decision. It holds the
AuthZEN request and response: the decision, and on a denial the policy's
reason code, which OpenFTV carries in `reason_user.en`.

The embedded OPA also writes a "Decision Log" line to stdout for every
evaluation, and promtail ships it to Loki. That line carries the whole policy
result, including which rule granted or denied each field. It is
observability, and nothing may cite it as the record of a decision. The
developer portal shows both side by side, the ADL record as the decision and
the console line as detail, and colours the PDP by the ADL record alone. It
reads the ADL with a SELECT-only role that `postgres-ftv-init` creates.

## Not observability: the Logboek Dataverwerkingen

Every Dataverwerking in the chain is also recorded in the logbook of the
Verantwoordelijke that performed it — `logboek-bd` for the Belastingdienst,
`logboek-brp` for RvIG, and for the voorziening itself `logboek-toestemming`
(consent register and portal) and `logboek-eudi-adapter`; the demo consumer
keeps its own, `logboek-afnemer`. Separate
stores, served by [`ldv-logboek`](services/ldv-logboek/README.md), implementing
Logius LDV v1.0.0. It uses the OpenTelemetry log-record shape, which makes it look like a
second trace exporter. It is not, and the difference is the point: a span here
is best-effort exhaust of a technical operation, sampled and short-lived,
while an LDV record is an administrative record that must exist for every
processing, is confirmed on write, and is never sampled.

Nothing in this document changes because of it. The collector, Jaeger, Loki
and Grafana are untouched; the logbooks are additional stores with their own
guarantees. A component that cannot write its record fails its request rather
than dropping the record — which is exactly what an observability pipeline
must never do, and exactly what this one must.

The two are joined by the trace id. An LDV record takes it over from
`traceparent`, as the standard requires. The `Fsc-Transaction-Id` is FSC's own
id per transaction. The ADL decision record is meant to carry both. Behind the
FSC Inway it does not yet: the Inway sends no `traceparent` to the PDP and
passes the transaction id as `X-Request-Id`, so the record gets a trace of its
own and no `adl.fsc.transaction_id`. Both values do sit in the headers of the
recorded request, which is where the portal finds the record. The portal's
**Logboek Dataverwerkingen** panel shows, per trace, the LDV records of every
Verantwoordelijke next to the FSC transaction records and the PDP decision.
How a reader follows a chain across logbooks is in
[`docs/ldv`](docs/ldv/README.md).
