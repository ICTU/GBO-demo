# Onboarding van bronmetadata

GBO configureert geen query of mapping. Bij onboarding legt GBO alleen de
identiteit van de bron en de gekozen transportranden vast. De bron publiceert
per attestation type de GraphQL-query, het GraphQL-endpoint, de mapping en de
wallet Type Metadata.

## Wat staat waar?

| Gegeven | Eigenaar | Locatie |
|---|---|---|
| Bron-OIN, naam en transportkeuze | GBO onboarding | `sources/<oin>.yaml` in deze demo; in productie in de onboardingopslag |
| Metadata-endpoint | GBO onboarding | `metadata_endpoint` in de bronregistratie |
| Dataservicereferentie bij FSC | GBO onboarding | `data_access.service_reference` |
| GraphQL-endpoint, query, parameters, mapping en Type Metadata | bron | document op het geregistreerde metadata-endpoint |
| Immutable wallet Type Metadata en activatierecord | GBO | onboardingopslag |
| Issuer-, reader- en statuscertificaten en private keys | bevoegde certificaatbeheerder | secretopslag; nooit in de bronmetadata of onboardingregistratie |

De bron hoeft geen GraphQL-schema te publiceren. GBO ondersteunt precies één
gekozen transportprofiel per endpoint; er is geen automatische fallback.

Een FSC-registratie ziet er bijvoorbeeld zo uit:

```yaml
source_oin: "99999999900000000200"
name: "Belastingdienst-mock"
metadata_endpoint:
  transport: "fsc"
  service_reference: "gbo-attestation-metadata"
  path: "/.well-known/gbo-attestations"
data_access:
  transport: "fsc"
  service_reference: "bri"
```

Het brondocument bevat per type onder meer:

```json
{
  "graphql": {
    "endpoint": "/graphql",
    "document": "query ...",
    "subject_variable": "bsn",
    "parameters": {},
    "result_pointer": "/data/result",
    "cardinality": "exactly_one"
  }
}
```

Voor FSC zijn metadata- en GraphQL-endpoints absolute URL-paden. GBO roept ze
aan via `outway/{service_reference}{path}`. Het profiel `https-mtls` is al in
het registratiemodel opgenomen en vereist absolute HTTPS-URL's, maar de
runtime-implementatie ontbreekt bewust nog en faalt gesloten. Voordat dit
profiel wordt aangezet moeten onder andere het PKI-profiel, OIN-controle,
clientcertificaatbeheer en intrekking zijn vastgesteld.

## Lokale demo

De volledige, idempotente demo voor Belastingdienst en BRP is:

```sh
make onboard-demo-sources
```

De losse stappen zijn bewust zichtbaar:

```sh
# Read-only: registratie ophalen en inhoud valideren.
make validate-source SOURCE=sources/99999999900000000200.yaml

# Alleen lokaal: expliciet ontwikkelcertificaten maken.
make provision-development-certificates SOURCE=sources/99999999900000000200.yaml

# Bestaande certificaten laden, Type Metadata materialiseren en activeren.
make onboard-source SOURCE=sources/99999999900000000200.yaml
```

`onboard-source` mint of vernieuwt geen certificaten. Ontbrekende, verlopen of
niet meer bij de configuratie passende certificaten blokkeren activatie. Een
bevoegde beheerder moet dan eerst buiten onboarding nieuwe certificaten
uitgeven. `provision-development-certificates` is uitsluitend een lokale
demo-vervanger voor die handmatige productiehandeling.

Losse onboarding kan eerst zonder writes worden getest met `DRY_RUN=true`.
Activatie faalt ook gesloten bij een gewijzigde payload onder dezelfde versie,
versieterugval, onbereikbare metadata of een endpoint dat niet bij het gekozen
transportprofiel past.

## Runtime en productie

De runtime leest transport, OIN, type-id, service-referenties en endpoints uit
de activatierecords; dit zijn geen losse deployment-env-vars. Zonder geldige
activatie is een type niet uitgiftebaar en er is geen legacyfallback.

De demo gebruikt `storage-backend=filesystem` en
`certificate-store=development-ca`. Deze zijn instelbaar met
`ONBOARDING_STORAGE_BACKEND`, `ONBOARDING_CERTIFICATE_STORE` en de gelijknamige
CLI-flags. De productie-implementatie voor goedgekeurde opslag en secrets is
nog niet gekozen. Productiecertificaten worden handmatig of via een apart,
bevoegd proces uitgegeven; nooit automatisch door een GitHub Action of
`onboard-source`.
