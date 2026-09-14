# Logboek Dataverwerkingen: een keten volgen

Dit document beschrijft hoe je een verwerking in de DvTP-keten terugvindt over
meerdere Logboeken Dataverwerkingen (LDV): welke id's de logboeken verbinden,
wie welke verwijzing zet en in welke volgorde je leest. Hoe een logboek zelf
werkt, staat in [`services/ldv-logboek`](../../services/ldv-logboek/README.md).

## Logboeken

Elk systeem schrijft naar het logboek van zijn Verantwoordelijke. De logboeken
zijn niet gefedereerd; wat ze verbindt is de trace-id.

| Logboek | Verantwoordelijke | Schrijvers |
|---|---|---|
| `logboek-afnemer` | Hypotheek-BV | `dienstverlener-backend` |
| `logboek-bd` | Belastingdienst | `bron-sidecar`, `graphql-server` |
| `logboek-brp` | RvIG | `brp-sidecar`, `brp-graphql-server` |
| `logboek-toestemming` | GBO | `consent-register`, `consent-portal-backend` |
| `logboek-eudi-adapter` | GBO | `eudi-adapter` |

Toestemmingsregister en -portaal delen één logboek. De extensie lezen telt elk
logboek als een aparte applicatie met een eigen lees-API; met één gedeeld
logboek blijven register en portaal één applicatie. De afnemer logt zijn eigen
verwerking in een eigen logboek; GBO logt niet namens hem.

## Twee id's

| | W3C trace-id `T` | `Fsc-Transaction-Id` `X` |
|---|---|---|
| Wie maakt hem | de applicatie die de verwerking start | de FSC-Outway |
| Aantal per verwerking | één | één per FSC-transactie |
| Staat in | LDV `trace_id`, ADL `trace_id` | FSC-txlog, ADL `adl.fsc.transaction_id` |

Elke applicatie neemt `T` ongewijzigd over uit `traceparent` en zet de span van
de aanroeper als `parent_span_id` (LDV §3.3.1). `X` is een transport-id. De ADL
van de PDP legt `T` en `X` samen vast, zodat je vanuit een LDV-record de
bijbehorende FSC-transactie vindt.

## De DvTP-bevraging

![De DvTP-bevraging: trace T door de keten, nextLogbookId van de afnemer naar de Belastingdienst, en geen pointer naar het toestemmingslogboek](doel.svg)

- De afnemer stuurt `traceparent` `T` en `Fsc-Transaction-Id` `X` mee. Achter
  FSC loggen de sidecar en de graphql-server onder `T`.
- De PDP legt zijn beslissing vast in de ADL, met `T` en `X`. De Inway zet de
  headers van het oorspronkelijke verzoek, `traceparent` inbegrepen, in de
  AuthZEN-context; daar leest de PDP `T` uit.
- De PDP vraagt de consentstatus op bij het register en stuurt `traceparent`
  mee. Het record in `logboek-toestemming` hangt onder de span van die
  opvraging.
- `processor` noemt in een record de applicatie die de verwerking aanriep. Het
  is een identificatie, geen leesroute.

### Een keten volgen

Lezen begint bij de applicatie die de verwerking startte en volgt
`nextLogbookId` (extensie lezen):

1. Vraag `logboek-afnemer` op `traceId` `T`.
2. Het record van de afnemer wijst met `nextLogbookId` naar de lees-API van de
   Belastingdienst. De afnemer haalt die URL uit de dienstbeschrijving van de
   bron.
3. `logboek-bd` geeft onder `T` de records van de sidecar en de graphql-server.
   Voor het transport koppelt de ADL `T` aan `X`, en de FSC-txlog kent `X`.
4. Naar `logboek-toestemming` wijst geen `nextLogbookId`. De statuscheck
   gebeurt in de PDP, en die schrijft een ADL, geen LDV. Het statusrecord vind
   je door `logboek-toestemming` op `T` te bevragen.

## Eén verwerking, meerdere bronnen

![De afnemer schrijft twee records onder trace T, elk met een nextLogbookId naar de bron die hij aanriep](meerdere-bronnen.svg)

Een record heeft één `nextLogbookId`, en die zet je bij iedere verwerking die
een externe partij aanroept. Bevraagt de afnemer twee bronnen, dan schrijft hij
twee records onder dezelfde `T`, elk met een eigen verwijzing. Elke aanroep is
een eigen FSC-transactie met een eigen `X`; `T` blijft gelijk.

Zonder de records van de afnemer is niet te zien dat de verwerking beide
bronnen raakte: elke bron ziet alleen haar eigen aanroep.

## Standaarden

- [Logboek Dataverwerkingen 1.0.0](https://gitdocumentatie.logius.nl/publicatie/logboek/dataverwerkingen/1.0.0/)
- [Extensie lezen](https://logius-standaarden.github.io/logboek-extensie-lezen/)
- [Authorization Decision Log](https://logius-standaarden.github.io/authorization-decision-log/)
- [FSC-Logging](https://logius-standaarden.github.io/fsc-logging/)
