# Bronmetadata-cache en Type Metadata

Fase 3 is standaard uitgeschakeld. De bestaande `SOURCE_METADATA_SHADOW_ENABLED`
blijft de terugvaloptie uit fase 1; `SOURCE_METADATA_CACHE_ENABLED=true` kiest in
plaats daarvan de vernieuwbare, fail-closed cache.

## Configuratie

| Variabele | Betekenis |
|---|---|
| `SOURCE_METADATA_CACHE_ENABLED` | Activeert refresh, versiecontrole en VCT-binding voor de pilot-usecase. |
| `SOURCE_METADATA_OUTWAY_PATH` | FSC-Outwaypad naar het bronendpoint `/.well-known/gbo-attestations`. |
| `SOURCE_METADATA_OIN` | Verwacht bron-OIN; moet overeenkomen met de ondertekende metadata. |
| `SOURCE_METADATA_PUBLIC_JWK_PATH` | Pad naar de tijdens onboarding gepinde publieke Ed25519-JWK van de bron. |
| `SOURCE_METADATA_TYPE_ID` | Te activeren type-id; tijdelijk één waarde, standaard `inkomensverklaring`. |
| `SOURCE_METADATA_USECASE_KEY` | Bestaande adapter-usecase waaraan dit type tijdelijk is gekoppeld. |
| `TYPE_METADATA_PUBLIC_BASE_URL` | Publieke basis-URL van de adapter, bijvoorbeeld de waarde van `EUDI_BRI_URL` zonder verplicht trailing slash. |
| `TYPE_METADATA_STORE_PATH` | Duurzame opslag voor immutable Type Metadata; standaard `/var/lib/gbo/type-metadata`. |

De enkelvoudige bron-, type- en usecasevelden zijn bewust tijdelijk; een latere
onboardingfase vervangt ze door een gevalideerde registratie voor meerdere
bronnen.

## Gedrag

- GBO haalt alleen via het geregistreerde FSC-pad op en stuurt na de eerste
  succesvolle fetch `If-None-Match` met de ETag van de bron.
- Iedere `200` wordt opnieuw op JWS, gepinde sleutel, bron-OIN, tijden, query,
  mapping en monotone bron- en typeversie gevalideerd; een `304` verlengt alleen
  de bestaande cachetermijn.
- GBO vereist bij acceptatie minimaal één uur resterende brongeldigheid,
  refresht iedere vijf minuten, vertrouwt een succesvolle refresh maximaal
  vijftien minuten als fresh en staat daarna maximaal één uur stale-grace toe.
- Voor iedere typeversie maakt GBO de VCT
  `/types/{bron-oin}/{type-id}/v{type-version}`, voegt deze aan Type Metadata
  toe en berekent `vct#integrity` over precies de bytes die via die URL worden
  geserveerd.
- De immutable bytes worden vóór activatie duurzaam opgeslagen. Een bestaande
  versie-URL kan nooit andere bytes krijgen; corrupte opslag blokkeert het laden.
- Buiten stale-grace antwoordt de geconfigureerde issuance-usecase met `503`.
  Reeds gepubliceerde Type Metadata-versies blijven wel beschikbaar voor
  bestaande credentials.

Cachemode laat het IssuableDocument `attestation_type` overeenkomen met de VCT
en voegt `vct#integrity` als te ondertekenen claim toe. De issuance-server moet
voor exact die VCT en typeversie zijn geprovisioned voordat de featureflag wordt
ingeschakeld; die lokale provisioning hoort bij fase 4.
