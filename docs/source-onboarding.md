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

## Issuance-configuratie

Onboarding schrijft een genegeerd
`.local/secrets/<oin>/issuance.env`. De configuratie kiest hiervoor expliciet
`storage-backend=filesystem` en `certificate-provider=development-ca`.
`make eudi-config` leest dit bestand na de
gewone `.env`, zodat de lokaal geminte issuer-, reader- en statuscertificaten en
hun lokale trust-anchors in de bestaande issuance-serverconfig terechtkomen.
URL-configuratie zoals `EUDI_PUBLIC_URL`, `EUDI_READER_ORIGIN_URL` en
`EUDI_BRI_URL` blijft deploymentconfiguratie in `.env`.

De door onboarding gepubliceerde Type Metadata en gepinde publieke bronkey
worden vanuit `.local/onboarding/` in de adapter gemount. De tijdelijke
`SOURCE_METADATA_*` featureflag blijft standaard uit voor rollback; zet
`SOURCE_METADATA_CACHE_ENABLED=true` om het bronmetadatapad te activeren.

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
