# Lokale onboarding van bronmetadata

Fase 4 gebruikt geen aparte onboardingservice. De bestaande `graphql-server`
publiceert bronmetadata en de bestaande `eudi-adapter` bevat twee eenmalige
commands:

- `validate-source` leest de Git-registratie, haalt de JWS via FSC op en
  valideert registratie, sleutelbinding, handtekening, schema, query, mapping,
  versies en geldigheid. Dit command schrijft niets.
- `onboard-source` herhaalt dezelfde validatie, maakt met de geconfigureerde
  `development-ca`-provider issuer-, reader- en statuscertificaten, publiceert
  immutable Type Metadata en
  schrijft pas als laatste de actieve registratie.

## Wat staat waar?

| Gegeven | Eigenaar | Locatie |
|---|---|---|
| OIN, naam, FSC-servicereferenties en JWK-thumbprint | GBO | `sources/<oin>.yaml` in Git |
| Query, mapping, types en Type Metadata-inhoud | bron | ondertekend `/.well-known/gbo-attestations` via de aparte FSC-service |
| Publieke metadata-verificatiesleutel en immutable Type Metadata | GBO onboarding | `.local/onboarding/` |
| Private metadata-, issuer-, reader- en statuskeys | lokale secret-backend | `.local/secrets/` (Git ignored, mode `0600`) |

De bron publiceert geen GraphQL-schema en er is geen publieke URL-fallback. De
registratie bevat alleen trust- en transportranden; GBO configureert geen query
of mapping.

## Verse lokale checkout

Start FSC en de bronmetadata-publicatie, en leg daarna het aparte
metadatacontract vast. `make fsc-all-up` maakt bij een verse checkout
automatisch een genegeerde `fsc-infra/.env` met een lokaal databasewachtwoord:

```sh
make fsc-all-up
make source-metadata-up
make fsc-seed-metadata
```

Valideer eerst zonder wijzigingen:

```sh
make validate-source SOURCE=sources/99999999900000000200.yaml
```

Bekijk vervolgens exact dezelfde onboarding zonder writes en voer haar daarna
uit:

```sh
make onboard-source SOURCE=sources/99999999900000000200.yaml DRY_RUN=true
make onboard-source SOURCE=sources/99999999900000000200.yaml
```

Het tweede command is idempotent. Een gewijzigde payload onder dezelfde versie,
een versieterugval, een verkeerde sleutel, een ongeldig certificaat of een
onbereikbaar FSC-endpoint faalt gesloten.

De bestanden onder `sources/` gebruiken bewust een beperkt, Git-beheerd
YAML-profiel: precies de vijf gedocumenteerde velden als scalars op het hoogste
niveau. Geneste waarden, arrays, multilinewaarden, anchors en tags worden niet
ondersteund. Eén ongeldige registratie blokkeert de validatie van de volledige
registratieset; dat is bewust fail-closed en het foutbericht noemt het bestand.

## Issuance-configuratie

Onboarding schrijft een genegeerd
`.local/secrets/<oin>/issuance.env`. De configuratie kiest hiervoor expliciet
`storage-backend=filesystem` en `certificate-provider=development-ca`.
`make eudi-config` gebruikt dit bestand alleen na expliciete opt-in:

```sh
make eudi-config USE_ONBOARDING_EUDI_ENV=true
```

Het command meldt dan dat de geminte issuer-, reader- en statuscertificaten en
hun lokale trust-anchors de certificaatwaarden uit `.env` overschrijven. Zonder
opt-in blijft `.env` leidend.
URL-configuratie zoals `EUDI_PUBLIC_URL`, `EUDI_READER_ORIGIN_URL` en
`EUDI_BRI_URL` blijft deploymentconfiguratie in `.env`.

De door onboarding gepubliceerde Type Metadata en gepinde publieke bronkey
worden vanuit `.local/onboarding/` in de adapter gemount. De tijdelijke
`SOURCE_METADATA_*` featureflag blijft standaard uit voor rollback; zet
`SOURCE_METADATA_CACHE_ENABLED=true` om het bronmetadatapad te activeren.
`SOURCE_METADATA_OIN` en `SOURCE_METADATA_PUBLIC_JWK_PATH` hebben in Compose
bewust geen demo-defaults: na inschakelen moeten de onboardingswaarden expliciet
worden ingesteld.

`make demo-eudi` en `make demo-full` maken de bind-mountdirectories vóór
Compose met de huidige gebruiker aan. Start je Compose rechtstreeks, voer dan
eerst `make onboarding-directories` uit om root-owned directories op Linux te
voorkomen.

De generieke CLI is ook het productie-entrypoint. Een PR-job voert alleen
`validate-source` uit; een goedgekeurde post-mergejob voert `onboard-source` uit
met productie-implementaties voor opslag, secrets en certificaten. De
`development-ca`-provider is uitsluitend voor ontwikkeling. Productieproviders
volgen pas nadat dezelfde flow lokaal end-to-end is bewezen en het
CA-/trustmodel met het Wallet-team is vastgesteld.

Dezelfde waarden zijn rechtstreeks te configureren met
`ONBOARDING_STORAGE_BACKEND` en `ONBOARDING_CERTIFICATE_PROVIDER`, of met de
CLI-flags `--storage-backend` en `--certificate-provider`. Onbekende waarden
worden vóór provisioning geweigerd.
