# DvTP-toestemmingsflow

Dit document beschrijft hoe een burger toestemming verleent, hoe een
dienstverlener die toestemming gebruikt en hoe intrekking onmiddellijk in het
autorisatiebesluit doorwerkt.

`S01` is de architectuurcode die in de demo voor het consent-register wordt
gebruikt. Het is geen afzonderlijke service naast het consent-register.

## Componenten

```mermaid
flowchart LR
    Citizen["Burger"]
    ConsumerUI["Dienstverlener frontend"]
    ConsumerBE["Dienstverlener backend"]

    subgraph ConsentDomain["Toestemmingsdomein"]
        Portal["Toestemmingsportaal"]
        BSNk["BSNk-mock"]
        Register["Consent-register S01"]
        ConsentDB["Consent-database"]
    end

    subgraph FSC["OpenFSC transport"]
        Outway["FSC Outway"]
        Inway["FSC Inway"]
    end

    subgraph Authorization["Autorisatie"]
        Mapper["OpenFTV mapper"]
        Policy["Rego-policy DVT0001"]
    end

    Source["Bron-sidecar en GraphQL-bron"]

    Citizen --> ConsumerUI
    ConsumerUI --> Portal
    Portal --> BSNk
    Portal --> Register
    Register --> ConsentDB
    Portal --> ConsumerUI
    ConsumerUI --> ConsumerBE
    ConsumerBE --> Outway
    Outway --> Inway
    Inway --> Mapper
    Mapper --> Register
    Mapper --> Policy
    Policy --> Inway
    Inway --> Source
    Source --> ConsumerBE
```

Het consent-register bewaart geen PI. De PI staat alleen in het ondertekende
consent-token en wordt tijdens het aanmaken daarvan kortstondig verwerkt. Voor
burgergerichte listing en ownership gebruikt het register een afzonderlijke,
portaalgebonden `subject_ref`.

## 1. Toestemming verlenen

```mermaid
sequenceDiagram
    autonumber
    actor Citizen as "Burger"
    participant UI as "Dienstverlener frontend"
    participant Portal as "Toestemmingsportaal"
    participant BSNk as "BSNk-mock"
    participant Register as "Consent-register S01"
    participant DB as "Consent-database"

    Citizen->>UI: Start aanvraag
    UI->>Portal: Vraag toestemming voor OIN en scopes
    Citizen->>Portal: Log in en bevestig toestemming

    Portal->>BSNk: Pseudonimiseer BSN voor dienstverlener
    BSNk-->>Portal: PI voor gegevensuitwisseling
    Portal->>BSNk: Pseudonimiseer BSN voor portaal
    BSNk-->>Portal: Portaalgebonden subject_ref

    Portal->>Register: Maak consent met PI, subject_ref, OIN en scopes
    Register->>DB: Bewaar consent zonder PI
    Register->>Register: Onderteken ES256 consent-token
    Register-->>Portal: Consent-id en consent-token

    Portal-->>UI: Redirect met consent-id en tokenfragment
    UI->>UI: Lees token en verwijder fragment uit URL
```

Het token bevat de bindings waarop de PDP later beslist:

- `consent_id` en `jti`;
- PI, scopes en optionele veldselecties;
- `dienstverlener_oin`;
- issuer, audience en geldigheid via `iat`, `nbf`, `exp` en `valid_until`.

De bestaande API-veldnaam `dienstverlener_oin` bevat in deze flow de FSC Peer
ID waarmee de PDP de token aan `subject.id` bindt. De voorgedefinieerde
issuance-scenario's van het developer portal vullen dit veld vanuit
`DVTP_CONSUMER_PEER_ID`; alleen lokaal geldt bij ontbrekende configuratie de
default `99999999900000000300`. Handmatig ingevoerde en opgeslagen payloads
worden niet door deze configuratie overschreven.

Het portaal accepteert alleen `http`- en `https`-return-URL's waarvan de
exacte origin in `VITE_ALLOWED_RETURN_ORIGINS` staat. De token wordt in het
URL-fragment teruggegeven en direct uit de zichtbare URL verwijderd.

## 2. Toestemming gebruiken

```mermaid
sequenceDiagram
    autonumber
    participant UI as "Dienstverlener frontend"
    participant BE as "Dienstverlener backend"
    participant OUT as "FSC Outway"
    participant IN as "FSC Inway"
    participant MAP as "OpenFTV mapper"
    participant Register as "Consent-register S01"
    participant POL as "Rego-policy DVT0001"
    participant SRC as "Gegevensbron"

    UI->>BE: Verstuur consent-token en gegevensvraag
    BE->>BE: Lees PI en scopes voor GraphQL-aanvraag
    Note right of BE: Dit is geen verificatie

    BE->>OUT: GraphQL met consent-token en scope
    OUT->>IN: Verstuur via FSC-contract en mTLS
    IN->>MAP: Vraag autorisatiebesluit met FSC-context

    opt Sleutelcache is leeg, verlopen of kid is onbekend
        MAP->>Register: Haal JWKS op
        Register-->>MAP: Publieke ES256-sleutels
    end
    MAP->>MAP: Verifieer token, bindings en tijdclaims

    MAP->>Register: Controleer status van exact consent-id
    Register-->>MAP: ACTIVE, REVOKED of niet gevonden

    MAP->>POL: Verstrek consent-, query- en FSC-context
    POL->>POL: Controleer actor, PI, scope en geldigheid

    alt Alle controles slagen
        POL-->>IN: ALLOW
        IN->>SRC: Voer GraphQL-aanvraag uit
        SRC-->>IN: Gegevens
        IN-->>BE: Gegevens
        BE-->>UI: Toon resultaat
    else Een controle faalt
        POL-->>IN: DENY met reden
        IN-->>BE: Toegang geweigerd
        BE-->>UI: Toon afwijzing en trace
    end
```

De mapper gebruikt een JWKS-cache, vernieuwt direct bij een onbekende `kid`
en kan een bekende sleutel tijdens een korte storing begrensd stale gebruiken.
De status van het specifieke consent wordt wel bij iedere aanvraag online
gecontroleerd. JWT-tijdclaims hebben 30 seconden tolerantie voor klokverschil.

De policy geeft alleen `ALLOW` wanneer alle volgende relaties kloppen:

```text
geldige ondertekening en geregistreerde claims
+ consent bestaat, is ACTIVE en is nog geldig
+ FSC subject.id == dienstverlener_oin uit het token
+ PI in de GraphQL-query == PI uit het token
+ scope, jaren en velden vallen binnen het consent
= ALLOW
```

Ontbrekende, ongeldige of niet-beschikbare context faalt gesloten met een
gerichte reden, zoals `CONSENT_CONTEXT_INVALID`, `CONSENT_ACTOR_MISMATCH`,
`CONSTRAINT_MISMATCH`, `CONSENT_SCOPE_MISMATCH`,
`CONSENT_STATUS_UNAVAILABLE` of `CONSENT_WITHDRAWN`.

## 3. Toestemming intrekken

```mermaid
sequenceDiagram
    autonumber
    actor Citizen as "Burger"
    participant Portal as "Toestemmingsportaal"
    participant BSNk as "BSNk-mock"
    participant Register as "Consent-register S01"
    participant PDP as "OpenFTV PDP"

    Citizen->>Portal: Open Mijn toestemmingen
    Portal->>BSNk: Leid portaalgebonden subject_ref af
    Portal->>Register: Zoek toestemmingen via subject_ref
    Register-->>Portal: Toestemmingen van deze burger

    Citizen->>Portal: Trek consent in
    Portal->>Register: Trek exact consent-id in
    Register->>Register: Zet status op REVOKED

    PDP->>Register: Controleer status bij volgende aanvraag
    Register-->>PDP: REVOKED
    PDP->>PDP: DENY met CONSENT_WITHDRAWN
```

Een ander actief consent voor dezelfde burger kan het ingetrokken consent niet
vervangen: de PDP controleert altijd uitsluitend het `consent_id` uit het
ondertekende token.

## Beveiligingsgrens

Het demo-token is een bearer-token. De actorbinding voorkomt dat een andere
FSC-consument het token gebruikt, maar maakt replay door dezelfde consument
niet onmogelijk. Een productie-implementatie moet daarom een kortere
tokenlevensduur en proof-of-possession aan de FSC- of mTLS-identiteit toevoegen.
