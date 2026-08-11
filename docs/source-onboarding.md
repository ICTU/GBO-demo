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

De complete idempotente demo-onboarding voor zowel Belastingdienst als BRP is:

```sh
make onboard-demo-sources
```

De target start FSC en de twee bronpublishers, legt beide metadata-contracten
vast en activeert daarna beide bronnen. `make fsc-all-up` maakt bij een verse
checkout automatisch een genegeerde `fsc-infra/.env` met een lokaal
databasewachtwoord.

Losse validatie zonder wijzigingen kan voor iedere bron:

```sh
make validate-source SOURCE=sources/99999999900000000200.yaml
make validate-source SOURCE=sources/99999999900000000400.yaml
```

Bekijk de losse onboarding eerst zonder writes en activeer daarna:

```sh
make onboard-source SOURCE=sources/99999999900000000200.yaml DRY_RUN=true
make onboard-source SOURCE=sources/99999999900000000400.yaml DRY_RUN=true
make onboard-source SOURCE=sources/99999999900000000200.yaml
make onboard-source SOURCE=sources/99999999900000000400.yaml
```

De activatiecommands zijn idempotent. Een gewijzigde payload onder dezelfde versie,
een versieterugval, een verkeerde sleutel, een ongeldig certificaat of een
onbereikbaar FSC-endpoint faalt gesloten.

De bestanden onder `sources/` gebruiken bewust een beperkt, Git-beheerd
YAML-profiel: precies de vijf gedocumenteerde velden als scalars op het hoogste
niveau. Geneste waarden, arrays, multilinewaarden, anchors en tags worden niet
ondersteund. Eén ongeldige registratie blokkeert de validatie van de volledige
registratieset; dat is bewust fail-closed en het foutbericht noemt het bestand.

## Issuance-configuratie

Onboarding schrijft per bron een genegeerd
`.local/secrets/<oin>/issuance.env`. De lokale configuratie kiest hiervoor
expliciet `storage-backend=filesystem` en
`certificate-provider=development-ca`. Nadat beide demo-bronnen zijn
geonboard, genereert dit de issuance-configuratie:

```sh
make eudi-config
```

Het command gebruikt de per bron geminte issuer- en readercertificaten, leest
VCT, adapterroute en Type Metadata-referentie uit de activatierecords,
controleert hun onderlinge binding en installeert de metadata in de
issuance-serverconfig. Er is geen alternatieve catalogusconfiguratie.
URL-configuratie zoals `EUDI_PUBLIC_URL`, `EUDI_READER_ORIGIN_URL` en
`EUDI_BRI_URL` blijft deploymentconfiguratie in `.env`.

De actieve registraties, gepubliceerde Type Metadata en gepinde publieke
bronkeys worden vanuit `.local/onboarding/` in de adapter gemount. OIN, type-id,
FSC-servicereferentie, parameters en JWK-pad worden uit ieder activatierecord en
de ondertekende bronmetadata afgeleid en zijn geen losse env-vars. Zonder een
geldige activatie is een type niet uitgiftebaar; er is geen legacyfallback.

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
