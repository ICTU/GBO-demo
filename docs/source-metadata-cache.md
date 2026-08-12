# Bronmetadata-cache en Type Metadata

Alle uitgifte loopt via geonboarde bronmetadata. Zonder geldige activaties start
de adapter wel met een health-endpoint, maar blijven attestatieverzoeken
fail-closed; er bestaat geen catalogus- of formatterfallback.

## Configuratie

| Variabele | Betekenis |
|---|---|
| `SOURCE_ACTIVATIONS_PATH` | Directory met één atomair activatierecord per geonboarde bron/type-combinatie. |
| `TYPE_METADATA_PUBLIC_BASE_URL` | Publieke basis-URL van de adapter, bijvoorbeeld de waarde van `EUDI_BRI_URL` zonder verplicht trailing slash. |
| `TYPE_METADATA_STORE_PATH` | Duurzame opslag voor immutable Type Metadata; standaard `/var/lib/gbo/type-metadata`. |

OIN, type-id, gekozen transport en eventuele FSC-servicereferentie worden uit het ene
activatierecord afgeleid en niet nogmaals als losse env-vars geconfigureerd.
De runtime scant alle `*.json`-activatierecords en activeert precies één type
per record. Het record bevat daarnaast de gevalideerde offers en verwijzingen
naar de vooraf beheerde issuer-, reader- en statuscertificaten.

## Gedrag

- GBO haalt alleen via het tijdens onboarding geregistreerde transport en
  metadata-endpoint op. Als de bron een ETag
  aanbiedt, stuurt GBO die bij de volgende refresh als `If-None-Match`; zonder
  ETag blijft de fetch onvoorwaardelijk en bewaakt GBO versies en payloaddigests.
- Iedere `200` wordt opnieuw op bron-OIN, tijden, query, mapping en monotone
  bron- en typeversie gevalideerd; een `304` verlengt alleen
  de bestaande cachetermijn.
- GBO vereist bij acceptatie minimaal één uur resterende brongeldigheid,
  refresht iedere vijf minuten, vertrouwt een succesvolle refresh maximaal
  vijftien minuten als fresh en staat daarna maximaal één uur stale-grace toe.
  De issuance-response maakt dit zichtbaar als
  `X-GBO-Metadata-Cache: fresh|stale`.
- Voor iedere typeversie maakt GBO de VCT
  `/types/{bron-oin}/{type-id}/v{type-version}`, voegt deze aan Type Metadata
  toe en berekent `vct#integrity` over precies de bytes die via die URL worden
  geserveerd.
- De immutable bytes worden vóór activatie duurzaam opgeslagen. Een bestaande
  versie-URL kan nooit andere bytes krijgen. De laatst geactiveerde bron- en
  typeversie plus digests worden eveneens duurzaam opgeslagen, zodat een
  restart geen rollback mogelijk maakt; corrupte opslag blokkeert het laden.
- Buiten stale-grace antwoordt de geactiveerde attestation-route met `503`.
  Reeds gepubliceerde Type Metadata-versies blijven wel beschikbaar voor
  bestaande credentials, ook wanneer de bronregistratie na een restart
  tijdelijk ongeldig is.
- `type_metadata.schema` is verplichte broninput en moet een JSON-object zijn;
  GBO voegt daarin zelf de regels voor `vct` en `vct#integrity` toe.
- Iedere `{{placeholder}}` in een wallet-`summary` moet een gelijknamige
  `svg_id` op een claim hebben; onboarding weigert anders de Type Metadata.
- Gepubliceerde typeversies worden bewust niet automatisch verwijderd. De
  beheerder moet de duurzame volumeomvang bewaken en pas een retentiebeleid
  toepassen wanneer geen geldig credential meer naar een versie kan verwijzen.

De geverifieerde bronmapping produceert rechtstreeks het IssuableDocument,
zonder bron-specifieke conversies in de adapter. Het `attestation_type` is de
geactiveerde VCT en `vct#integrity` wordt als claim meegegeven.

## Issuance-artifacts

`make eudi-config` leest alle activatierecords en genereert de volledige
bronafhankelijke startupconfiguratie van de issuance-server. Voor ieder offer
ontstaat een disclosure-product met de geactiveerde VCT, adapterroute en
parameterwaarden. Dezelfde stap installeert de geactiveerde Type Metadata en
maakt `eudi-offers.json` voor de QR-keuzelijsten. Er is geen tweede, statische
usecase- of jaarcatalogus in de adapter of issuance-server.

De gebruikte nl-wallet issuance-server herlaadt deze configuratie niet
dynamisch. Een activatiewijziging vereist daarom regeneratie en een rollout van
issuance-server en frontends. Voor productie moet de rollout bovendien borgen
dat de runtimequery en de gegenereerde issuance-config uit hetzelfde
activatiesnapshot komen; die snapshotkoppeling is nog een expliciet
vervolgpunt.
