# ldv-logboek — Logboek Dataverwerkingen

One image, one instance per **Verantwoordelijke**: `logboek-bd`
(Belastingdienst), `logboek-brp` (RvIG) and `logboek-gbo` (the voorziening
itself). Nothing in the code knows about a particular organisation — an
instance is "the Belastingdienst's" because of the register document it serves
and the components configured to write to it.

LDV has each Verantwoordelijke log its own processing, with only trace
metadata crossing a boundary, so a shared logbook would be the wrong shape
however convenient it looks in a demo. What ties the three together is one
trace id, not one store.

Implements the write side of [Logius LDV
v1.0.0](https://logius-standaarden.github.io/LDV/): the third of the three
logging standards this chain follows, alongside the Authorization Decision Log
(PDP decisions) and FSC-Logging (transport transactions).

## What this would be in production

You would not build this service. It exists here because the demo simulates
three organisations at once, and each of them needs a logbook to write to.

**Ownership.** LDV puts the logbook with the Verantwoordelijke. In production
the Belastingdienst and RvIG each run their own; GBO neither builds nor
operates those. GBO runs one, for its own processing, and ships the *client* —
the contract about record shape, mandatory attributes, confirmation and no
sampling — to everyone else in the chain. Three near-identical instances is a
simulation artefact, not an architecture proposal.

**What you would run instead.** The record is OTel-shaped and the standard
recommends OTLP, so the obvious route is an OTLP pipeline with a deliberately
non-sampling collector configuration and an audit store behind it — not Loki.
The catch is the requirement that makes LDV different: confirmation per record.
A default collector batches and queues, so the producer gets an acknowledgement
that guarantees nothing. That is configurable, but it has to be designed rather
than assumed, which is why this demo speaks plain HTTP: the confirmation is
then unmistakable. An organisation that already has a verwerkingenlogging
facility should adapt to it; the LDV interface is small enough.

**Two ways this design would not survive production.**

*The logbook is on the critical path.* Fail-closed means a logbook outage is a
service outage, so it must be at least as available as everything it logs —
a heavy demand for a single-instance SQLite service. The better shape is a
local durable spool: the component writes the record to its own disk, is
confirmed there, and forwards asynchronously. The hard guarantee survives —
nothing lost, nothing sampled — without coupling availability to a remote
service. What the producer needs confirmed is "durably recorded", not "durably
recorded in the remote logbook". This demo does not make that distinction.

*Nothing here is tamper-evident.* The store accepts `UPDATE` and `DELETE` like
any other SQLite database, and a logbook an operator can edit is not evidence.
Production wants WORM storage, hash chaining or an external notary. That sits
under the same governance question as retention (Q-08), but it is also a
storage decision made early rather than late.

## Why this is not the observability pipeline

LDV reuses the OpenTelemetry log-record shape, which invites the conclusion
that the existing OTel spans could be relabelled. They cannot (REQ-32):

| | OTel spans → Jaeger/Loki | LDV records → logboek |
| --- | --- | --- |
| about | technical operations | Dataverwerkingen |
| completeness | best-effort | every processing, or the processing does not happen |
| sampling | allowed | never |
| write | fire-and-forget, batched | confirmed synchronously |
| retention | short | audit lifecycle |

Same pipes, different content and different guarantees. Loki, Jaeger and
Grafana are untouched by this service; the logbooks are additional, separate
stores.

## The record

An OTel-shaped log record with the mandatory fields — `trace_id`, `span_id`,
`status`, `name`, `start_time`, `end_time`, `attributes`, optionally
`parent_span_id` and `resource` — and three mandatory `dpl.core.` attributes:

```json
{
  "trace_id": "0af7651916cd43dd8448eb211c80319c",
  "span_id": "b7ad6b7169203331",
  "parent_span_id": "00f067aa0ba902b7",
  "name": "dataverwerking.bronbevraging",
  "status": "OK",
  "start_time": "2026-09-02T10:00:00Z",
  "end_time": "2026-09-02T10:00:00.012Z",
  "resource": { "service.name": "graphql-server" },
  "attributes": {
    "dpl.core.processing_activity_id": "bd-ib-2025@v1",
    "dpl.core.data_subject_id": "PI-abc123",
    "dpl.core.data_subject_id_type": "pi",
    "dpl.core.foreign_operation.processor": "fsc-peer:AAAABBBBCCCCDDDDEEEE",
    "gbo.belastingjaar": 2025
  }
}
```

`trace_id` **is** the `Fsc-Transaction-Id`, hyphens stripped — a UUID is
exactly 32 hex characters, so LDV's `traceID`, the ADL's trace id and the FSC
transaction log all carry one value for one request (REQ-55). That is a
mitigation as much as a design: FSC v2.4.0 drops `traceparent` between peers,
so the correlation handle has to travel in a field FSC does propagate.

### Never the BSN

`dpl.core.data_subject_id` carries a pseudonym, never a BSN (REQ-60/REQ-72),
and `dpl.core.data_subject_id_type` says which pseudonym space it lives in:

| type | meaning |
| --- | --- |
| `pi` | the polymorphic identity the DvTP chain carries end to end |
| `logboek-pseudoniem` | a key-derived, logbook-local reference, for components that hold only a BSN (the EUDI flow) |
| `portal-subject` | the portal-scoped reference the consent portal derives, and the only identifier the consent register ever holds |
| `brp-persoon-id` | RvIG's own record identifier, for someone named in a certificate about another person |

`brp-persoon-id` is deliberately not a pseudonym. It names a Betrokkene the
source has no pseudonym for — a relative of the deceased, who never asked for
anything — and it is acceptable precisely because the record never leaves
RvIG's own logbook. A record crossing an organisation boundary would need a
pseudonym.

The pseudonym key is per Verantwoordelijke, not shared: one key would make the
same citizen recognisable across organisations' logbooks, which is exactly
what a logbook-local pseudonym must not do.

`logboek-pseudoniem` is a demo stand-in: HMAC-SHA-256 over the BSN with a
per-Verantwoordelijke key, truncated. Stable within one Verantwoordelijke,
meaningless outside it. A real chain would name the Betrokkene by a pseudonym
it was *given*; the fact that the EUDI flow has none to give is a gap this
makes visible rather than papers over.

The logbook does not take the producers' word for it. Every write is scanned
for a nine-digit run that passes the elfproef — in attribute values (including
numbers and nested structures), the record name and the resource — and refused
if one is found. A pseudonymisation bug in one component therefore fails
loudly at the boundary instead of writing a BSN into a store meant to be
retained.

## The verwerkingsactiviteiten register (a stand-in)

Every record must name a **resolvable, versioned** verwerkingsactiviteit. That
presumes a Register van Verwerkingsactiviteiten, which this demo does not have.

Instead each logbook serves a small versioned document, generated from what we
do have — the dienstencatalogus scope definitions (`bd:ib:2025`,
`bd:ib:2024`) plus the infrastructural processings around them — and validates
every write against it:

```
GET /verwerkingsactiviteiten              → the index
GET /verwerkingsactiviteiten/bd-ib-2025@v1 → one entry
```

**This is not an RvVA.** It has no legal status. Whether the real thing extends
the dienstencatalogus or becomes a separate facility is an open question; the
stand-in exists to make that gap tangible without blocking, and every served
entry carries a disclaimer saying so.

One register per Verantwoordelijke, in [`config/`](config/). The
Belastingdienst's ([`verwerkingsactiviteiten-bd.json`](config/verwerkingsactiviteiten-bd.json)):

| reference | Dataverwerking | logged by |
| --- | --- | --- |
| `bd-pi-bsn-resolutie@v1` | PI → BSN resolution | `bron-sidecar` |
| `bd-bronquery-doorgifte@v1` | receiving and forwarding a bronbevraging | `bron-sidecar` |
| `bd-ib-2025@v1` | verstrekking inkomensgegevens IB 2025 | `graphql-server` |
| `bd-ib-2024@v1` | verstrekking inkomensgegevens IB 2024 | `graphql-server` |

RvIG's ([`verwerkingsactiviteiten-brp.json`](config/verwerkingsactiviteiten-brp.json)):

| reference | Dataverwerking | logged by |
| --- | --- | --- |
| `brp-pi-bsn-resolutie@v1` | PI → BSN resolution | `brp-sidecar` |
| `brp-bronquery-doorgifte@v1` | receiving and forwarding a bronbevraging | `brp-sidecar` |
| `brp-akte-overlijden@v1` | verstrekking akte van overlijden | `brp-graphql-server` |
| `brp-persoonsgegevens-verstrekking@v1` | verstrekking BRP-persoonsgegevens | `brp-graphql-server` |

GBO's own ([`verwerkingsactiviteiten-gbo.json`](config/verwerkingsactiviteiten-gbo.json)):

| reference | Dataverwerking | logged by |
| --- | --- | --- |
| `gbo-bsn-pseudonimisering@v1` | BSN → PI + portal-scoped reference at consent intake | `consent-portal-backend` |
| `gbo-toestemming-verlenen@v1` | recording a consent | `consent-register` |
| `gbo-toestemming-intrekken@v1` | revoking a consent | `consent-register` |
| `gbo-toestemming-status@v1` | confirming a consent's status to the PDP | `consent-register` |
| `gbo-toestemming-inzage@v1` | showing a citizen their own consents | `consent-register` |
| `gbo-pid-bsn-extractie@v1` | reading the BSN out of a disclosed PID | `eudi-adapter` |
| `gbo-attestatie-samenstellen@v1` | assembling an attestation from source data | `eudi-adapter` |

A component names the activity it performs; the logbook refuses a reference
its register does not resolve, and that refusal fails the request. So a
verstrekking whose verwerkingsactiviteit nobody wrote down does not happen —
which is the register requirement actually biting rather than being described.

## API

| | |
| --- | --- |
| `POST /logboek/records` | write one record. `201` with a confirmation, `200` when the (trace_id, span_id) was already stored, `400` on malformed JSON, `422` on an unlawful record, `401` without the bearer token. |
| `GET /verwerkingsactiviteiten` | register index (unauthenticated) |
| `GET /verwerkingsactiviteiten/{ref}` | one entry (unauthenticated) |
| `GET /health` | liveness |

Transport is plain HTTPS+JSON. The standard leaves the protocol free — OTLP is
only RECOMMENDED — and JSON keeps the demo inspectable with `curl`. A
deliberate simplification, not a reading of the standard.

`POST` returns only after the record is committed with `synchronous = FULL`, so
a confirmation means the record survives an unclean shutdown. `(trace_id,
span_id)` is the primary key, so a producer's retry after a timeout is
idempotent and is reported as a duplicate rather than creating a second
Dataverwerking.

Access control is minimal on purpose: network-internal plus a shared bearer
token. Who may write to and read from a Verantwoordelijke's logboek is an open
governance question (Q-08), and answering it with an invented demo scheme
would be worse than saying so.

## Configuration

| variable | default | |
| --- | --- | --- |
| `PORT` | `4016` | |
| `DATABASE_PATH` | `/data/logboek.db` | SQLite, like `dvtp-onboarding-register` |
| `REGISTER_PATH` | `/config/verwerkingsactiviteiten.json` | which Verantwoordelijke this instance is |
| `LDV_WRITE_TOKEN` | — | required; the service refuses to start without it |

## Producers: fail-closed

A component joins an LDV chain by having `LDV_LOGBOOK_URL` set. Once it is,
a processing that cannot be logged **does not deliver**: the sidecar withholds
the source response, the bron buffers its GraphQL answer until the records are
confirmed and returns `500` otherwise. There is no third mode where records
are dropped quietly — that is precisely the guarantee LDV adds over an
observability pipeline.

With `LDV_LOGBOOK_URL` unset a component is not in an LDV chain and writes no
records at all. That is how `unsecured-graphql-server`, which runs the same
image as `graphql-server`, stays out of it.

Producer configuration:

| variable | |
| --- | --- |
| `LDV_LOGBOOK_URL` | the logbook of this component's Verantwoordelijke; unset means no LDV |
| `LDV_WRITE_TOKEN` | must match the logbook's |
| `LDV_SUBJECT_PSEUDONYM_KEY` | key for `logboek-pseudoniem` derivation |
| `LDV_RESOLUTION_ACTIVITY`, `LDV_FORWARD_ACTIVITY` | `bron-sidecar`/`brp-sidecar` only — the same image runs in front of every bron, and each bron's register names its activities in its own terms |
| `LDV_YEAR_ACTIVITY_TEMPLATE` | `graphql-server` only, e.g. `bd-ib-%d@v1` |

`consent-register` and `consent-portal-backend` need no
`LDV_SUBJECT_PSEUDONYM_KEY`: neither ever holds a BSN in a record, so there is
nothing to derive a pseudonym from.

## The client

[`services/ldv-client`](../ldv-client) is a sibling Go module, pulled in with a
path `replace` rather than published and versioned: a change to the client has
to be usable by the services in the same commit, without a tag in between.

Its consequence is that the services depending on it build from the repository
root — the same arrangement `eudi-adapter` and `dev-portal-backend` already
use, since they bake in `./policies`. A `.dockerignore` keeps that context from
including the nl-wallet submodule.

The module owns everything that is the same everywhere: configuration and the
"not in an LDV chain" case, the register fetch, the confirmed write, trace and
span ids, the attribute map, the FSC claim decoding, and the subject
derivation for components that hold a BSN. `ldvtest` in the same module is the
fake logbook the services' tests drive. What stays per service is what only
that service knows: which of its verwerkingsactiviteiten a given request
performed and who it was about — a small wrapper type embedding the client.

`consent-portal-backend` carries none of it. It is laid out as ports and
adapters, so its core owns a `Logbook` port and `ldv/` implements it with a
purpose-built writer: no FSC boundary, no BSN, and therefore no use for the
shared client's machinery.

## What the flows produce

A DvTP query, in `logboek-bd`:

```
Fsc-Transaction-Id 0af76519-…-c80319c
└── bd-bronquery-doorgifte@v1   bron-sidecar   subject PI-abc123 (pi)
    ├── bd-pi-bsn-resolutie@v1  bron-sidecar   subject PI-abc123 (pi)
    └── bd-ib-2025@v1           graphql-server subject PI-abc123 (pi)
```

The same trace id appears on the ADL decision record written by the OpenFTV
PDP and in the FSC transaction log of both peers.

The death-certificate attestation, in `logboek-brp`. One request, three
Betrokkenen: the surviving partner who asked, and the two living relatives the
certificate names. The relatives' records hang under the requester's because
they exist only as part of that one processing.

```
└── brp-bronquery-doorgifte@v1  brp-sidecar        LP-4727… (logboek-pseudoniem)
    └── brp-akte-overlijden@v1  brp-graphql-server LP-4727… (logboek-pseudoniem)   rol=aanvrager
        ├── brp-akte-overlijden@v1  brp-graphql-server 018f2c4a-…-000a (brp-persoon-id)  rol=ouder-van-overledene
        └── brp-akte-overlijden@v1  brp-graphql-server 018f2c4a-…-000b (brp-persoon-id)  rol=ouder-van-overledene
```

The deceased is **not** among them. The AVG protects living persons, so the
person the certificate is about is not a Betrokkene of this processing even
though their data is what gets disclosed. The requester's record carries
`gbo.akte.overledene_verwerkt` so a reader sees that this was decided rather
than forgotten.

A consent lifecycle, in `logboek-gbo` — each step under the portal-scoped
reference, which is the only identifier this side ever holds:

```
gbo-bsn-pseudonimisering@v1  consent-portal-backend  EP-3f9a… (portal-subject)
gbo-toestemming-verlenen@v1  consent-register        EP-3f9a… (portal-subject)
gbo-toestemming-status@v1    consent-register        EP-3f9a… (portal-subject)
gbo-toestemming-intrekken@v1 consent-register        EP-3f9a… (portal-subject)
```

The status check is logged like any other processing rather than treated as a
read-only lookup: it is what makes a revocation take effect.

```bash
docker compose exec -T logboek-bd \
  wget -qO- http://localhost:4016/verwerkingsactiviteiten
```

## Scope

Phases 1 and 2 of [#302](https://github.com/ICTU/GBO-demo/issues/302) cover the
write side for every in-scope component. Not here yet:

- **Phase 3** — the read extension (`extensie lezen`) per logbook,
  `dpl.read.nextLogbookId` on cross-org records, and a developer-portal panel
  showing the three-standard picture per trace. The store already indexes the
  three query axes that extension needs.

Out of scope entirely: retention terms, bewaarplicht, append-only and signing
guarantees — governance questions where the LDV *profielen* mechanism is meant
to land; a real RvVA; wallet-side processing; and the consumer's own
processing.

That last one is a boundary rather than a gap. `dienstverlener-backend`
receives and processes response data, which is a Dataverwerking — but of
Hypotheek-BV, not of anything GBO delivers. Its logbook would be Hypotheek-BV's
own, and instrumenting the mock consumer here would suggest the voorziening
logs on a consumer's behalf, which is exactly what LDV says it must not do.
