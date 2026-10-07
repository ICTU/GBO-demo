# FTV GraphQL-profiel in GBO

Dit document beschrijft hoe de demo het *FTV GraphQL Profile for NLgov
AuthZEN* (draft-01) implementeert: per eis waar en hoe eraan voldaan wordt,
en wat nog openstaat. Sectienummers verwijzen naar het profiel.

Het profiel beantwoordt één vraag: welke velden mag een afnemer bij een bron
opvragen? De PDP krijgt de GraphQL-query als ruwe body, zet die met het
schema om in een veldenlijst, beslist per veld en geeft één besluit voor het
hele verzoek. De bron voert alleen uit wat de PDP heeft beoordeeld.

```text
afnemer → Outway → Inway (PEP) → OpenFTV PDP: mapper ftvgraphql → Rego-engine
                        ↓ allow
                   bron-sidecar → GraphQL-server (BD, BRP, LVG)
```

## PEP: de FSC-Inway (§5)

| Eis | Hoe |
|---|---|
| Elk verzoek voor de bron gaat naar de PDP, met ruwe body (§5.1, §5.2) | `AUTHZEN_WITH_BODY=true` op elke Inway (`fsc-infra/docker-compose.yml`). De Inway stuurt de body alleen mee bij `application/json`; zonder body faalt de mapper dicht |
| `subject.id` uit mTLS, `resource.id` het ruwe request-target, headers, `X-Request-ID` (§5.2) | standaardgedrag van de Inway; `X-Request-ID` is de `Fsc-Transaction-Id` |
| Bij deny 401 of 403, niet doorsturen (§10.3) | de Inway antwoordt 401 en stuurt niets door |
| `graphql.admin` nooit naar de afnemer (§10.3) | OpenFTV geeft alleen `allow` en `reason` door; `reason` is de afnemerscode |

## Mapper (§5.1, §6)

`services/ftv-graphql-mapper` is de kern, zonder OpenFTV-afhankelijkheid.
`services/openftv-pdp/mapper/ftvgraphql.go` hangt hem als request-mapper
`ftvgraphql` in de PDP (`PDP_REQUEST_MAPPINGS`). De uitvoer komt in
`input.resource.attributes.graphql`: OpenFTV zet `resource.properties` daar
neer.

| Eis | Hoe |
|---|---|
| Alleen de transportsubset: POST, `application/json`, exact het GraphQL-pad (§5.1) | `transport.go`; anders `COVERAGE_UNVERIFIABLE UNSUPPORTED_TRANSPORT` |
| Strikte body: grootte vóór parsen, geen extra of dubbele leden, exacte getallen (§6.1) | `body.go` |
| Heel document valideren, operatie kiezen op `operationName`, mutation en subscription weigeren (§6.3, §6.4.1) | `validate.go`, `mapper.go` |
| Eén record per selectie, parent-type uit het schema, pad op response-keys (§6.4, §6.6) | `walk.go` |
| Variabelen en defaults coërceren, met herkomst per waarde (§6.5) | `coerce.go`: `origin` is `literal`, `variable:<naam>` of `schema-default` |
| Limieten per stap (§6.7) | `Limits` in `mapper.go` |

De testvectoren van Appendix A staan in `vectors_test.go`.

## Schema (§7)

| Eis | Hoe |
|---|---|
| Het SDL waartegen de regels zijn geschreven, zonder executable directives (§7.1) | `schemas/pdp-mirror`, per FSC-service in `services.json` |
| Buiten de bundel geladen, dus digest gepind (§7.1, §7.3) | het SDL zit in het PDP-image; de bundel pint de digest in `policies/dvtp/gbo/graphql_schemas.rego` (`scripts/gen-graphql-pins.sh`, CI controleert actualiteit) |
| Geen schema ophalen of introspectie op runtime (§7.1) | de mapper leest alleen de bestanden in het image |
| Drift tussen kopie en bron blokkeert (§7.4, H3) | `scripts/check-schema-drift.sh` start de drie bronnen en vergelijkt elk element van de kopie met hun introspectie; CI-job `schema-drift`. Beleid en bronnen staan in één repository, dus één job dekt beide pijplijnen |

## Policy-engine (§9)

`policies/dvtp/gbo/engine.rego` voert de controles uit, de regels staan in
`policies/dvtp/gbo/rules/`.

| Eis | Hoe |
|---|---|
| Eerst het verzoek: mapperuitvoer aanwezig, digest gepind, verifieerbaar, datavelden over (§9.5) | in die volgorde: `CONFIG_ERROR MAPPER_OUTPUT_MISSING`, `CONFIG_ERROR SCHEMA_MISMATCH`, de `unverifiable`-code van de mapper, `NO_DATA_FIELDS` |
| Closed world: elk dataveld heeft een regel, `__typename` en introspectie-internals vallen af (§9.2, §9.3) | een veld zonder regel is `NO_APPLICABLE_RULE` |
| Binding op `ParentType.field`, rootvelden zijn datavelden (§9.3) | regels noemen velden of typen; argumentbindingen per veld |
| Nooit toestaan op een ontbrekend argument of een schema-default (§9.4) | `argument()` in `lib.rego` is ongedefinieerd voor een waarde met herkomst `schema-default` |
| PIP faalt: veld `PIP_UNAVAILABLE` (§9.4, §12) | toestemmingsregister, zijn sleutels en het toelatingsregister |
| Alle datavelden AND (§9.5) | één geweigerd veld weigert het verzoek |

De tests van Appendix A staan in `appendix_a_test.rego`.

## Besluit en foutmelding (§10)

| Eis | Hoe |
|---|---|
| `reason_user`, `reason_admin`, `graphql.client`, `graphql.admin` per klasse (§10.1, §10.2) | `graphql.client` is wat de afnemer hoort: bij een serverprobleem `ACCESS_DENIED`, bij een geweigerd veld `FIELD_NOT_PERMITTED`. `graphql.admin` bevat de echte code, de mappermelding, de schemadigest en per geweigerd veld de regels met hun trace |
| De afnemer krijgt de afnemerscode (§10.3) | `policies/authz.rego` zet `reason` op code en subcode van `graphql.client` |
| Wat de burger ziet | alleen `FIELD_NOT_PERMITTED` wordt doorgegeven, met de vraag opnieuw toestemming te geven; de rest wordt `UNAVAILABLE` (`dienstverlener-backend/consumer/denial.go`) |

## De bron (H1, H2)

| Eis | Hoe |
|---|---|
| De bron voert exact uit wat de PDP beoordeelde en accepteert alleen de transportsubset (H1) | de `bron-sidecar` is de voordeur van de bron en weigert alles daarbuiten met 404, 405, 400 of 415, vóór ontsleutelen of doorsturen. Het mediatype beoordeelt hij met de functie van de mapper zelf |
| Niets tussen PEP en bron verandert het verzoek (H1) | de sidecar hoort bij de bron: hij vult na het besluit alleen de placeholder `consent:identity` in met het BSN uit het toestemmingstoken; query en `operationName` blijven gelijk |
| De bron kiest dezelfde operatie (H2) | standaard GraphQL-gedrag van de bronnen |

## Open punten

- **De Inway laat de reden vallen.** Hij leest `reasonUser` waar de PDP
  `reason_user` stuurt, dus de afnemer krijgt geen code. Upstream opgelost,
  nog niet uitgebracht; tot dan ziet de burger de algemene melding.
- **De Inway vervangt ongeldige UTF-8** door U+FFFD in de body die hij de
  PDP stuurt (§5.2). De PDP beoordeelt dan andere bytes dan de bron krijgt.
- **Het deny-antwoord is geen GraphQL-`errors[]`** maar de FSC-foutbody
  (§10.3, SHOULD).
- **Het besluitlog (§10.4) is onvolledig.** Het Authorization Decision Log
  van OpenFTV legt het oorspronkelijke verzoek en het besluit vast, niet het
  verrijkte verzoek en niet `graphql.admin`. Het console-besluitlog van de
  ingebedde OPA bevat beide en de developer portal toont het, maar dat is
  observability, geen vastlegging.
- **Geen `traceparent` van de Inway.** Correlatie loopt via `X-Request-ID`
  (de `Fsc-Transaction-Id`), wat het profiel toestaat.

Buiten het profiel, eigen aan GBO: de header `X-GBO-Scope` (een gedeclareerde
scope die de regels tegen de toestemming toetsen; wordt verwijderd) en de
uitzondering in `policies/authz.rego` voor het bronmetadatadocument
(`GET /.well-known/gbo` op de metadataservices, zonder GraphQL).
