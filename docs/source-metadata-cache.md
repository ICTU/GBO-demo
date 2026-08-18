# Levenscyclus van bronmetadata en Type Metadata

Alle uitgifte loopt via geonboarde bronmetadata. Zonder geldige actieve
snapshots start de adapter wel met een health-endpoint, maar blijven
attestatieverzoeken fail-closed. Er bestaat geen catalogus- of
formatterfallback.

## Procesconfiguratie

| Proces | Instelling | Betekenis |
|---|---|---|
| Reconciler | `--type-metadata-base-url` | Publieke basis-URL van de adapter; lokaal afgeleid uit `EUDI_BRI_URL`. |
| Reconciler | `--state-dir` | Duurzame opslag met `candidates/`, `active/`, `status/` en `type-metadata/`. |
| Issuance-runtime | `SOURCE_ACTIVATIONS_PATH` | Alleen-lezen directory met één uitgerold record per logische bron. |
| Issuance-runtime | `TYPE_METADATA_STORE_PATH` | Alleen-lezen opslag voor immutable Type Metadata; standaard `/var/lib/gbo/type-metadata`. |

OIN, type-id, transport, query, mapping en eventuele FSC-servicebinding worden
uit het activatierecord gelezen en niet als losse runtimevariabelen
geconfigureerd. Eén record bevat alle typen en offers van een logische bron en
de verwijzingen naar de vooraf beheerde issuer-, reader- en
statuscertificaten.

## Ophalen en geldigheid

- Alleen de reconciler haalt bronmetadata op, via het handmatig geregistreerde
  transport en metadata-endpoint. De issuance-runtime pollt bronnen of FSC
  nooit zelf.
- Met een ETag stuurt de reconciler bij de volgende controle
  `If-None-Match`. Een `304` verlengt alleen de cachetermijn; iedere `200` wordt
  opnieuw volledig gevalideerd.
- GBO weigert versieterugdraaiing en gewijzigde bytes onder dezelfde versie.
  Een ongeldige kandidaat vervangt de laatste complete kandidaat niet.
- Bij acceptatie moet minimaal één uur brongeldigheid resteren. Een succesvolle
  controle is maximaal vijftien minuten fresh en daarna maximaal één uur stale,
  nooit langer dan `expires_at` van de bron.
- De actieve issuance-route toont de toestand als
  `X-GBO-Metadata-Cache: fresh|stale` en antwoordt buiten stale grace met
  `503`.

## Type Metadata

- Voor iedere typeversie maakt GBO de VCT
  `/types/{source-id}/{type-id}/v{type-version}`.
- GBO voegt `vct` en `vct#integrity` toe en berekent de integriteitswaarde over
  precies de immutable bytes die via die VCT-URL worden geserveerd.
- `type_metadata.schema` is verplichte broninput en moet een JSON-object zijn.
- Iedere `{{placeholder}}` in een wallet-`summary` moet een gelijknamige
  `svg_id` op een claim hebben.
- Gepubliceerde typeversies worden niet automatisch verwijderd. Een
  retentiebeleid mag een versie pas verwijderen wanneer geen geldig credential
  er nog naar kan verwijzen.

De gevalideerde `gbo-simple-v1`-mapping produceert rechtstreeks het
`IssuableDocument`, zonder bron-specifieke conversies in de adapter. nl-wallet
zet `vct` vanuit `attestation_type` en gebruikt de geïnstalleerde Type
Metadata-bytes voor `vct#integrity`; beide worden daarom niet als gewone
attributes meegestuurd.

## Kandidaten en rollout

De reconciler schrijft complete bronversies naar `candidates/` en een duurzame
status per bron naar `status/`. Een inhoudelijke wijziging aan typen, offers,
VCT's of certificaatverwijzingen krijgt de status `rollout_required` en raakt
de actieve uitgifte nog niet.

`make eudi-config` controleert dat iedere handmatig geconfigureerde bron een
bruikbare kandidaat heeft. Daarna genereert het de nl-wallet-producten, Type
Metadata en `eudi-offers.json` en promoveert het de volledige kandidaatset
atomair naar `active/`. Een `pending`, `stale` of `blocked` bron laat deze stap
falen, zodat een bron niet stil uit het aanbod verdwijnt.

De gebruikte nl-wallet issuance-server herlaadt zijn productconfiguratie niet
dynamisch. Een inhoudelijke wijziging vereist daarom regeneratie en een
gecontroleerde rollout van issuance-server en frontends. In productie moeten
de gegenereerde artifacts en het bijbehorende actieve snapshot onderdeel zijn
van dezelfde release voordat verkeer naar de nieuwe pods gaat.
