# Observability

The local Compose environment and the simulation deployment use the same
three observability data paths:

1. Application traces are sent over OTLP to a gateway collector.
2. The collector stores traces in Jaeger and also forwards them to
   `dev-portal-backend/v1/traces` for the live architecture strip.
3. Container logs are shipped to Loki. The developer portal queries the OPA
   decision logs in Loki, while Grafana provides the broader log view.

The simulation platform currently has no stable, tenant-consumable core OTLP
or Loki endpoint in its tenant contract. The fallback is therefore deployed
once alongside the developer portal in MinEZK and shared with MinBZK, rather
than duplicated per tenant.

OpenFSC does not currently propagate the OpenTelemetry trace context across
the Inway boundary. The applications therefore also record
`gbo.fsc.transaction_id`. The developer portal uses this value to correlate
the consumer trace, the PDP trace and the FSC transaction logs.

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
| `LOKI_URL` | Loki base URL |
| `LOKI_DECISION_QUERY` | LogQL selector for OPA decision logs |
| `FSC_TXLOG_*_URL/CERT/KEY/CA` | Local txlog-api or remote Manager logging source |

`BD_HV` and `BD_EDI` are optional provider-log sources. They allow the same
provider Manager to be queried from the authorized perspective of the DvTP
and EUDI consumer peers respectively.

## Data handling

The PDP removes BSN, PI values, authorization headers, tokens and credential
material from JSON copied into logs or trace attributes. The request sent to
OPA is unchanged. OPA additionally applies `data.system.log.mask` before it
emits a decision log.

Jaeger and Loki are debugging stores, not audit stores. The simulation
deployment uses seven-day retention; OpenFSC transaction logs remain the
authoritative per-hop message metadata.

## Not observability: the Logboek Dataverwerkingen

Every Dataverwerking in the chain is also recorded in the logbook of the
Verantwoordelijke that performed it — `logboek-bd` for the Belastingdienst,
`logboek-brp` for RvIG, `logboek-gbo` for the voorziening itself. Separate
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

The two are joined by one value: the trace id of an LDV record is the
`Fsc-Transaction-Id`, the same identifier the ADL decision record and the FSC
transaction logs carry, and the same one the developer portal already
correlates on. The portal's **Logboek Dataverwerkingen** panel makes that
visible: per trace it shows the LDV records of every Verantwoordelijke next to
the FSC transaction records and the PDP decision, all under one id.
