# Bronnen configureren en activeren

Een handmatig beheerd bestand in `sources/configured/` is de enige trigger om
een bron te onboarden. Een FSC-contract alleen maakt dus nooit een bron aan.
Iedere logische bron heeft een eigen `source_id` en verwijst naar een vooraf
beheerde issuer-, reader- en statuscertificaatset. De reconciler mint of
vernieuwt geen certificaten.

De bron blijft eigenaar van `/.well-known/gbo`. Dat document bevat per type de
GraphQL-query, parameters, concrete walletaanbiedingen, mapping en Type
Metadata. GBO bevat geen bron- of jaarcatalogus en de bron publiceert geen
GraphQL-schema.

## Transportprofielen

### FSC

```yaml
source_id: belastingdienst
provider_peer_id: "0000009958MINBZK0000"
source_oin: "99999999900000000200"
name: Belastingdienst
certificate_set: belastingdienst
metadata_endpoint:
  transport: fsc
  service_reference: gbo-metadata-bd
  path: /.well-known/gbo
data_access:
  transport: fsc
```

GBO resolveert zowel het geconfigureerde metadatacontract als de dataservice
uit actuele FSC-contracten. `provider_peer_id` selecteert de FSC-provider van
beide contracten en mag exact twintig alfanumerieke tekens bevatten.
`source_oin` is daarvan bewust gescheiden: dit blijft de numerieke, twintigcijferige
juridische identiteit die ook in het brondocument en de certificaten staat.
Grant-hashes worden bij iedere reconciliation opnieuw vastgelegd.

Meerdere logische bronnen mogen hetzelfde FSC-Peer ID gebruiken. `source_id`, de
metadata-service en certificaatset blijven per bron uniek. De lokale
Belastingdienst- en RvIG-bronnen gebruiken dit model.

### Unsecured

De meegeleverde unsecured voorbeeldbron staat bewust in
`sources/local-demo/`. Alleen de lokale Docker Compose-configuratie monteert
dit bestand. Simulatie- en productieconfiguraties gebruiken uitsluitend
`sources/configured/` en bieden deze bron dus niet aan.

```yaml
source_id: demo-unsecured
source_oin: "99999999900000000900"
name: Demo unsecured source
certificate_set: demo-unsecured
metadata_endpoint:
  transport: unsecured
  endpoint: http://unsecured-graphql-server:4000/.well-known/gbo
data_access:
  transport: unsecured
```

`unsecured` accepteert absolute HTTP- en HTTPS-endpoints. GBO stuurt geen
FSC-headers en volgt geen redirects. De OIN in het document moet nog steeds
overeenkomen met de handmatige configuratie, maar de transportlaag
authenticeert die identiteit niet. De status bevat daarom altijd
`transport_authenticated: false`.

Dit profiel is bedoeld voor demonstraties of afgeschermde netwerken. HTTPS
maakt het profiel niet brongebonden: zonder apart vastgelegde client- of
serveridentiteit blijft het `unsecured`. Autorisatie van het dataverzoek blijft
de verantwoordelijkheid van het gepubliceerde bronendpoint.

## Proces

```mermaid
sequenceDiagram
    participant O as Operator configuration
    participant R as Source reconciler
    participant F as FSC Manager/Outway
    participant B as Source
    participant C as Candidate store
    participant G as Issuance config generator
    participant A as Active store
    participant I as Adapter and issuance server

    O->>R: Configure source_id, transport and certificate_set
    R->>R: Check existing certificate set
    alt FSC
        R->>F: Resolve metadata contract by provider_peer_id
        R->>F: Fetch /.well-known/gbo with pinned grant
        F->>B: Authenticated FSC request
        R->>F: Resolve data contract from validated metadata
    else unsecured
        R->>B: Fetch absolute HTTP(S) metadata endpoint
    end
    R->>R: Validate schema, OIN, lifetime, query, offers, mapping and Type Metadata
    R->>C: Write complete candidate atomically
    R-->>O: rollout_required
    G->>C: Verify every configured source is complete
    G->>G: Generate issuance TOML, Type Metadata and offer catalog
    G->>A: Promote the complete candidate set atomically
    G-->>I: Controlled rollout of one consistent snapshot
```

De reconciler draait bij startup, daarna periodiek met `--watch`, of eenmalig
met `reconcile-sources --once`. In watch-modus worden de configuratiebestanden
bij iedere cyclus opnieuw gelezen. `ETag`/`If-None-Match`, monotone versies,
immutable bytes, freshness en stale grace worden door de reconciler bewaakt;
de HTTP-issuance-runtime haalt zelf nooit bronmetadata op.

Per bron staat de actuele toestand in
`.local/onboarding/status/<source_id>.json`:

- `pending`: de controle is gestart;
- `rollout_required`: een complete kandidaat wacht op configgeneratie;
- `active`: kandidaat en uitgerolde snapshot zijn gelijk;
- `stale`: ophalen of valideren faalt, maar de vorige snapshot zit nog binnen
  stale grace;
- `blocked`: er is geen bruikbare snapshot of stale grace is verstreken.

Een fout bij één bron blokkeert andere bronnen niet. `make eudi-config` faalt
wel wanneer een handmatig geconfigureerde bron `pending`, `stale` of `blocked`
is; een bron kan daardoor niet stil uit de walletproducten verdwijnen.

## Wat staat waar?

| Gegeven | Eigenaar/bron |
|---|---|
| `source_id`, `provider_peer_id`, verwachte `source_oin`, naam, certset en metadata-endpoint | GBO-bronconfiguratie |
| FSC provider Peer ID, services en grant-hashes | actuele FSC-contracten |
| GraphQL-endpoint, query, parameters, offers en mapping | `/.well-known/gbo` van de bron |
| Kaartnaam, kleur, kaartlogo, claimlabels en claimschema | Type Metadata in het brondocument |
| Getoonde issuernaam en issuerlogo | vooraf beheerde issuer-/readercertificaten |
| Immutable Type Metadata en kandidaat-/actiefsnapshot | GBO-onboardingopslag |
| Issuance-producten en QR-keuzelijst | mechanisch gegenereerd uit alle kandidaten |
| Private keys en certificaten | bevoegde certificaatbeheerder/secretopslag |

De maximale mappingfunctionaliteit staat in
[`gbo-simple-v1.md`](gbo-simple-v1.md). Onbekende functies en velden worden
geweigerd. Source-owned resolvers met domeinselectie blijven security-sensitive
broncode en moeten fail-closed regressietests hebben.

## Lokaal

Plaats voor een echte testwallet eerst de reeds vertrouwde ontwikkel-CA's in
`.local/secrets/development-ca/`, of zet `ONBOARDING_SECRETS_DIR` op een
beheerde secrets-root met de submap `development-ca/`; zie de
[Quick start](../README.md#quick-start). Onboarding genereert uitsluitend
brongebonden leafcertificaten. De issuer- en reader-CA worden nooit gegenereerd
en ontbrekende CA-bestanden laten de setup direct stoppen.
Daarna voert dit target de volledige onboarding uit:

```sh
make onboard-demo-sources
make eudi-config
```

Het eerste target start de drie bronnen, maakt de FSC-contracten, maakt
expliciet lokale leafcertificaten voor `belastingdienst`, `rvig` en
`demo-unsecured`, en draait één reconciliation. Het tweede target genereert de
walletproducten en promoveert alle kandidaten naar `active`.

## Productie

Gebruik hetzelfde adapterimage voor twee afzonderlijke workloads:

- één reconciler-Deployment met `reconcile-sources --watch` en één replica, of
  een CronJob met `--once`;
- de stateless HTTP-adapter die alleen de uitgerolde `active/`-snapshot leest.

Beide gebruiken dezelfde duurzame onboardingopslag. Voorbeeldwaarden staan in
[`source-reconciler-values.yaml`](../deploy/helm/gbo-app/examples/source-reconciler-values.yaml)
en [`eudi-adapter-values.yaml`](../deploy/helm/gbo-app/examples/eudi-adapter-values.yaml).
Certificaatprovisioning blijft een afzonderlijk, bevoegd beheerproces. Alleen
dat proces heeft de CA-private keys nodig. De reconciler-runtime krijgt de
issuer-, reader- en status-leaf keys/certificaten plus de publieke issuer- en
reader-CA-certificaten; CA-private keys horen niet in het runtime Secret.

### Upgrade vanaf v0.6.1

De scheiding tussen FSC-identiteit en juridische bronidentiteit wijzigt enkele
deploymentcontracten bewust fail-closed:

- iedere FSC-bronconfiguratie krijgt `provider_peer_id`; `source_oin` blijft
  numeriek en wordt niet vervangen door het Peer ID;
- de reconciler gebruikt `--consumer-peer-id` of
  `FSC_CONSUMER_PEER_ID`; `--consumer-oin` en `ISSUER_OIN` vervallen;
- het DvTP-register krijgt een JSON-configuratie via
  `ONBOARDING_CONFIG_PATH`, met `source_holders`, `system_participants` en
  optionele `seed_participants`;
- de OpenFTV PIP-feed gebruikt `peer_id` en `allowed_source_peer_ids`.

Rol eerst de DvTP-configuratie en de nieuwe register-/PDP-images samen uit.
Laat daarna de source reconciler ten minste één succesvolle ronde uitvoeren
voordat de HTTP-adapter als functioneel wordt beschouwd; zo worden bestaande
activaties aangevuld met `provider_peer_id`. De runtime kan zonder CA-private
keys starten zodra alle brongebonden leafbestanden en beide publieke
CA-certificaten aanwezig zijn.

Voor Kubernetes staan voorbeelden in
[`dvtp-onboarding-register-values.yaml`](../deploy/helm/gbo-app/examples/dvtp-onboarding-register-values.yaml),
[`dvtp-onboarding-configmap.yaml`](../deploy/helm/gbo-app/examples/dvtp-onboarding-configmap.yaml),
[`source-reconciler-values.yaml`](../deploy/helm/gbo-app/examples/source-reconciler-values.yaml)
en [`eudi-adapter-values.yaml`](../deploy/helm/gbo-app/examples/eudi-adapter-values.yaml).
