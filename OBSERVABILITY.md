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

## Runtime configuration

The production developer-portal image accepts these runtime variables:

| Variable | Purpose |
| --- | --- |
| `JAEGER_PUBLIC_URL` | Public Jaeger UI used by trace links |
| `GRAFANA_PUBLIC_URL` | Public Grafana UI used by log links |
| `EUDI_PUBLIC_URL` | Public issuance-server URL |
| `EUDI_CLIENT_ID` | Wallet reader client ID |

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
