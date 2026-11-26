# data-usage-index

The data usage index records that data was delivered: under legal basis X,
source Y delivered data under trace Z, at time T. It is a list of pointers. It
holds no personal data and nothing from the source; what was delivered stays in
the source's own Logboek Dataverwerkingen. Issue #687 calls it the trace index.

Today the only legal basis is a consent, and the consent register uses the index
to show a citizen when a consent was used. The name is wider than consent on
purpose: deliveries on another basis (EUDI issuance, OOTS) are meant to be
registered here too.

GBO runs it next to the consent register, under the register's FSC peer
(`99999999900000000700`).

## Two FSC services

| Port | FSC service (behind `cr-inway`) | Who has a connection             | What it serves                                                         |
| ---- | ------------------------------- | -------------------------------- | ---------------------------------------------------------------------- |
| 4019 | `data-usage-index`              | the source peers                 | `POST /registrations`                                                  |
| 4020 | `data-usage-lookup`             | the consent register's peer only | `GET /registrations?grondslag_type=consent&grondslag_ref={consent_id}` |

Both listeners serve `/health`, which checks the store. They are two services
because an Inway forwards every path under a service's endpoint: on one
listener, every source with a connection for registering could read the
registrations of any consent.

The lookup is an FSC service although register and index are one organisation:
provider and consumer of that contract are the same peer, and the register
reaches it through its own Outway (`cr-outway`). There is no shared token.
`LOOKUP_PEERS` names the peers that may look up, as a second check next to the
contract.

## A registration

```json
{
  "logbook_id": "99999999900000000200/ldv-lezen-bd",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "grondslag_type": "consent",
  "grondslag_ref": "c-0b8f…"
}
```

- `logbook_id` is the source peer's Peer ID, a slash and the name of the logbook
  on that peer. The name is needed because one peer can run several: the
  Belastingdienst peer `…0200` serves both `bd` and `lvg`.
- `trace_id` is the W3C trace-id of the delivery, 32 lowercase hex.
- `grondslag_type` is `consent`, the only legal basis the index knows;
  `grondslag_ref` is the `consent_id`.

The index answers 201 for a new registration and 200 with the stored one for a
retry: a registration is unique on logbook, trace and legal basis. It answers
401 for a request without an FSC access token, 403 when the calling peer (`sub`
of `Fsc-Authorization`) is not the peer in `logbook_id`, and 400 for anything
malformed.

The index does not check the consent. Whether it allowed the delivery was the
PDP's decision at the source's Inway, before the delivery happened.

## What it leaves out

- No data subject fields. #687 lists them, but nothing can fill them until BSNk
  pseudonyms reach the index.
- No lookup by data subject, no access tokens issued by the index, no direct
  access by MijnOverheid: the citizen reaches the uses through the register.
- No expiry. How long registrations are kept is a governance question (GBO-TO
  Q-08).

## Demo shortcuts

- Both listeners trust the `Fsc-Authorization` header the Inway forwards,
  without checking its signature, and are also reachable on the compose
  networks. A real deployment lets only the Inway reach them, as for the
  register's status listener.

## Run and test

```bash
go test ./...
```

`make demo-dvtp` builds and starts it, and `make fsc-seed-data-usage-index`
publishes both services over FSC, connects `bd-outway` and `brp-outway` for
registering and `cr-outway` for the lookup.
