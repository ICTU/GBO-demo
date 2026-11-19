# consent-usage-index

The consent usage index lets a citizen see when a consent was used. It is a
list of pointers: under consent X, source Y delivered data under trace Z, at
time T. It holds no personal data and nothing from the source; what was
delivered stays in the source's own Logboek Dataverwerkingen. Issue #687
calls it the trace index.

GBO runs it next to the consent register, under the register's FSC peer
(`99999999900000000700`).

## Two listeners

| Port | Who calls it | What it serves |
| --- | --- | --- |
| 4019 | the sources, over FSC through `cr-inway` (service `consent-usage-index`) | `POST /registrations` |
| 4020 | the consent register only, with `INDEX_READ_TOKEN` | `GET /registrations?grondslag_type=consent&grondslag_ref={consent_id}` |

Both serve `/health`, which checks the store. The lookup has a listener of its
own because an Inway forwards every path under a service's endpoint: on one
listener, every source with a connection could read the registrations of any
consent.

## A registration

```json
{
  "logbook_id": "99999999900000000200/bd",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "grondslag_type": "consent",
  "grondslag_ref": "c-0b8f…"
}
```

- `logbook_id` is the source peer's Peer ID, a slash and the name of the
  logbook on that peer. The name is needed because one peer can run several:
  the Belastingdienst peer `…0200` serves both `bd` and `lvg`.
- `trace_id` is the W3C trace-id of the delivery, 32 lowercase hex.
- `grondslag_type` is `consent`, the only legal basis the index knows;
  `grondslag_ref` is the `consent_id`.

The index answers 201 for a new registration and 200 with the stored one for
a retry: a registration is unique on logbook, trace and legal basis. It
answers 401 for a request without an FSC access token, 403 when the calling
peer (`sub` of `Fsc-Authorization`) is not the peer in `logbook_id`, and 400
for anything malformed.

The index does not check the consent. Whether it allowed the delivery was the
PDP's decision at the source's Inway, before the delivery happened.

## What it leaves out

- No data subject fields. #687 lists them, but nothing can fill them until
  BSNk pseudonyms reach the index.
- No lookup by data subject, no access tokens issued by the index, no direct
  access by MijnOverheid: the citizen reaches the uses through the register.
- No expiry. How long registrations are kept is a governance question
  (GBO-TO Q-08).

## Demo shortcuts

- Port 4019 trusts the `Fsc-Authorization` header the Inway forwards, without
  checking its signature, and is also reachable on the compose networks. A
  real deployment lets only the Inway reach it, as for the register's status
  listener.
- The lookup is protected by one shared bearer token. A real deployment would
  use mutual TLS or a workload identity between register and index.

## Run and test

```bash
go test ./...
```

`make demo-dvtp` builds and starts it, and `make fsc-seed-consent-usage-index`
publishes it over FSC and connects `bd-outway` and `brp-outway` to it.
