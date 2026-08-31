# Levenscyclus van bronmetadata en Type Metadata

Alle uitgifte loopt via geonboarde bronmetadata. Zonder geldige actieve
snapshots start de adapter wel met een health-endpoint, maar blijven
attestatieverzoeken fail-closed. Er bestaat geen catalogus- of
formatterfallback.

## Procesconfiguratie

| Proces | Instelling | Betekenis |
|---|---|---|
| Reconciler | `--type-metadata-base-url` | Publieke basis-URL van de adapter; lokaal afgeleid uit `EUDI_BRI_URL`. |
| Reconciler | `SOURCE_REGISTRY_DATABASE_URL` | Read-write PostgreSQL-verbinding voor kandidaten, statussen en atomische promotie. |
| Adapter | `SOURCE_REGISTRY_DATABASE_URL` | Alleen-lezen verbinding; de adapter wisselt zijn complete in-memory release atomair. |
| Alle registry-clients | `SOURCE_REGISTRY_SCHEMA` | Logisch schema, standaard `source_registry`. |
| Adapter | `SOURCE_REGISTRY_REFRESH_INTERVAL` | Interval waarmee de actieve releasepointer en lichte lifecycle-observaties worden gecontroleerd. |
| Issuance init-container | `SOURCE_REGISTRY_DATABASE_URL` | Alleen-lezen verbinding om één actieve release te materialiseren. |

OIN, type-id, transport, query, mapping en eventuele FSC-servicebinding worden
uit het activatierecord gelezen en niet als losse runtimevariabelen
geconfigureerd. Eén `SourceRelease` bevat immutable typen, offers en publieke
certificaatidentiteiten van de volledige bronset. De observatietijden voor
freshness en stale grace horen niet bij de release-identiteit en mogen bij een
succesvolle hercontrole worden verlengd. Private keys en secretpaden hebben
geen representatie in het registry-model.

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

## Kandidaten, releases en rollout

De reconciler schrijft complete kandidaten en de actuele status per bron naar
PostgreSQL. `--auto-promote` bouwt alleen wanneer *iedere* handmatig
geconfigureerde bron bruikbaar is een `SourceRelease`. De release-inhoud en
release-ID blijven gelijk wanneer een `304` alleen de observatietijden
verlengt; de bestaande lifecycle-kolommen worden dan bijgewerkt zonder nieuwe
release of Type Metadata-kopieën te maken. De release en de singleton
`active_release_id` worden in één transactie geschreven. Een `pending`,
`stale` of `blocked` bron verhindert promotie, zodat een bron niet stil uit het
aanbod verdwijnt. Een mislukte promotie laat de vorige actieve release
ongemoeid.

De adapter controleert de actieve pointer en bouwt een nieuwe release volledig
in geheugen voordat hij die atomair wisselt. Bij hetzelfde release-ID worden
alleen de freshness- en stale-grace-tijden in een nieuwe in-memory snapshot
overgenomen; Type Metadata en offers worden niet opnieuw geladen.
Attestatieafhandeling, `eudi-offers.json` en Type Metadata komen daardoor altijd
uit dezelfde release en vereisen geen restart.

De gebruikte nl-wallet issuance-server leest TOML alleen bij startup. Een
init-container leest de actieve release en combineert die met het gemounte
certificaat-Secret in een pod-lokale `emptyDir`. Een gewijzigde release vereist
daarom nog steeds een gecontroleerde restart van de issuance-server. Lokaal
voert `make eudi-config` die materialisatie en restart uit. Automatisch een
rollout triggeren blijft bewust buiten deze implementatie.

Rollback wijzigt uitsluitend `active_release_id` naar een bewaarde vorige
release. De adapter neemt die wijziging live over; de issuance-server moet
daarna opnieuw worden gestart.
