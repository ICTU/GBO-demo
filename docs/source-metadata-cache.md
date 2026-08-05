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

- GBO haalt alleen via het geregistreerde FSC-pad op. Als de bron een ETag
  aanbiedt, stuurt GBO die bij de volgende refresh als `If-None-Match`; zonder
  ETag blijft de fetch onvoorwaardelijk en bewaakt GBO versies en payloaddigests.
- Iedere `200` wordt opnieuw op JWS, gepinde sleutel, bron-OIN, tijden, query,
  mapping en monotone bron- en typeversie gevalideerd; een `304` verlengt alleen
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
- Buiten stale-grace antwoordt de geconfigureerde issuance-usecase met `503`.
  Reeds gepubliceerde Type Metadata-versies blijven wel beschikbaar voor
  bestaande credentials, ook wanneer de bronregistratie na een restart
  tijdelijk ongeldig is.
- `type_metadata.schema` is verplichte broninput en moet een JSON-object zijn;
  GBO voegt daarin zelf de regels voor `vct` en `vct#integrity` toe.
- Gepubliceerde typeversies worden bewust niet automatisch verwijderd. De
  beheerder moet de duurzame volumeomvang bewaken en pas een retentiebeleid
  toepassen wanneer geen geldig credential meer naar een versie kan verwijzen.

Cachemode laat het IssuableDocument `attestation_type` overeenkomen met de VCT
en voegt `vct#integrity` als te ondertekenen claim toe. De issuance-server moet
voor exact die VCT en typeversie zijn geprovisioned voordat de featureflag wordt
ingeschakeld; die lokale provisioning hoort bij fase 4.

Cachemode gebruikt in deze fase nog het legacy formatterresultaat en labelt dat
met de nieuwe VCT. Daarom blijft de flag uit totdat fase 4 expliciet kiest voor
de bronprojectie als credentialinhoud, of de legacy-only claims en datatypes met
de Type Metadata in overeenstemming brengt. Zonder die keuze mag de VCT niet in
de issuance-server worden geprovisioned.
