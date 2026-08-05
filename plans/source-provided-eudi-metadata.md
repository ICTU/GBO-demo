# Plan: Brongeleverde GraphQL → EUDI-configuratie

> Bron: [gbo-analysis issue 127](https://github.com/pjay-io/gbo-analysis/issues/127)

## Doel

Een bron kan zelfstandig attestaties, GraphQL-query's, mappings en inhoudelijke Type Metadata aanbieden. GBO onboardt alleen de bron en voert de generieke trust-, autorisatie-, projectie-, publicatie- en issuanceflow uit. Een tweede eenvoudige bron vereist geen bron-specifieke adaptercode.

## User stories

- **US1 — Bron beheert configuratie:** als bronhouder wil ik query's, mappings en attestatietypen zelf publiceren.
- **US2 — GBO onboardt alleen de bron:** als GBO-beheerder wil ik alleen identiteit en trustgrenzen registreren.
- **US3 — Veilige dynamische metadata:** als GBO wil ik bronmetadata ondertekend ophalen, valideren, versioneren en cachen.
- **US4 — Fijnmazige autorisatie:** als securitybeheerder wil ik iedere concrete query op bron, VCT, actor, velden en argumentwaarden toetsen.
- **US5 — Begrensde mapping:** als reviewer wil ik dat mapping nooit uitgroeit tot domeinlogica of uitvoerbare code.
- **US6 — Verifieerbare Type Metadata:** als wallet/verifier wil ik immutable Type Metadata met een kloppende `vct#integrity` kunnen ophalen.
- **US7 — Brongebonden signing:** als bronhouder wil ik dat credentials aantoonbaar namens mijn organisatie worden ondertekend zonder dat private keys de secret-backend verlaten.
- **US8 — Lokaal reproduceerbaar:** als ontwikkelaar wil ik de volledige onboarding- en issuanceflow lokaal kunnen testen zonder GitHub Actions.
- **US9 — Generieke tweede bron:** als ontwikkelaar wil ik een tweede bron aansluiten zonder nieuwe adapterlogica.

## Architecturale beslissingen

- **Bronmetadata-route:** iedere bron biedt `GET https://<bron>/.well-known/gbo-attestations` aan als ondertekende JWS.
- **Bronregistratie:** GBO bewaart uitsluitend bron-OIN, naam, metadata-URL, gepinde metadata-verificatiesleutel en FSC-servicereferentie.
- **Geen adapter-usecasecatalogus:** query en mapping komen uit bronmetadata; walletprotocol en signing blijven bij de issuance-server; autorisatie blijft bij GBO-policy.
- **Policycontext:** de PDP beslist minimaal op `source_oin`, `vct`, actor, `resolved.fields` en `resolved.args`; `scope` en `flow` zijn geen onderdeel van de doelarchitectuur.
- **Mappingprofiel:** `gbo-simple-v1` ondersteunt alleen pointer-gebaseerd kopiëren, hernoemen, flattenen, typecontrole en expliciet toegestane scalaire conversies.
- **Domeinlogica:** selectie, joins, sortering, eligibility en afgeleide domeinclaims leven in een domain-ready resolver bij de bron.
- **Cardinaliteit:** na de bronquery moet exact één resultaat resteren; nul is `no data`, meer dan één is ambigu en fail-closed.
- **Type Metadata:** de bron levert alle semantiek en presentatie-informatie; GBO voegt alleen de mechanisch bepaalde `vct` toe.
- **VCT-route:** GBO publiceert immutable Type Metadata onder `https://<issuer>/types/<bron-oin>/<type-id>/v<versie>`.
- **Integriteit:** GBO berekent `vct#integrity` zelf over de exact gepubliceerde bytes; de issuance-server neemt `vct` en `vct#integrity` vóór ondertekening op.
- **Cache:** bronmetadata wordt op bronversie en payloaddigest gecachet; GBO bepaalt refresh-, geldigheids- en stale-grenzen.
- **Certificaten:** onboarding roept een verwisselbare certificaatprovider aan; lokaal is dat een ontwikkel-CA, productie gebruikt later de afgesproken GBO/bron- of Wallet-beheerde CA.
- **Keybeheer:** issuer-, reader- en statuskeys worden in de gekozen secret-backend gegenereerd en verlaten die niet.
- **FSC-contract:** lokaal uitlezen via de bestaande mTLS Manager-API wordt getest; de gereviewde metadata-URL blijft fallback zolang CI-bereikbaarheid niet is vastgesteld.
- **Migratie:** nieuwe en oude uitvoering bestaan tijdelijk naast elkaar voor vergelijking en rollback; hardcoding wordt pas na aangetoonde pariteit verwijderd.

---

## Fase 1: Walking skeleton voor inkomensverklaring 2025

**User stories:** US1, US3, US4, US9

### Wat te bouwen

Laat de Belastingdienst-testbron één ondertekende attestatiedefinitie publiceren voor inkomensverklaring 2025. De adapter haalt deze definitie op, materialiseert de query met het disclosed BSN en belastingjaar, laat de volledige query door de bestaande PDP en FSC-keten lopen en projecteert het antwoord. De nieuwe uitvoering draait achter een lokale featureflag naast de bestaande hardcoded uitvoering en vergelijkt beide resultaten.

### Acceptatiecriteria

- [ ] Het bronendpoint levert een verifieerbare JWS met bron-OIN, bronversie, query, mapping, outputschema en Type Metadata-inhoud.
- [ ] De adapter weigert een ongeldige handtekening, verkeerde bron-OIN of syntactisch ongeldige GraphQL-query vóór een bronaanroep.
- [ ] De gematerialiseerde query bevat exact het disclosed BSN en het gevraagde belastingjaar.
- [ ] De PDP ziet de volledige query, opgeloste velden en argumentwaarden vóór uitvoering.
- [ ] De bestaande en nieuwe uitvoering leveren voor dezelfde testdata dezelfde attributen.
- [ ] De featureflag kan zonder datamigratie terugschakelen naar het bestaande pad.

---

## Fase 2: `gbo-simple-v1` en fail-closed projectie

**User stories:** US5, US9

### Wat te bouwen

Maak het maximale mappingprofiel normatief en machineleesbaar. Valideer iedere mapping zowel bij metadata-acceptatie als vóór activatie en voer uitsluitend de toegestane pointer- en conversieoperaties uit. Complexe of onbekende functionaliteit wordt niet geïnterpreteerd maar geweigerd.

### Acceptatiecriteria

- [ ] Er is normatieve documentatie voor alle operators, datatypes, geldschalen, foutcodes en grenzen van `gbo-simple-v1`.
- [ ] Een JSON Schema valideert het profiel en staat geen onbekende properties of operators toe.
- [ ] Conformance-tests bevatten geldige en ongeldige voorbeelden die onafhankelijk van de adapter uitvoerbaar zijn.
- [ ] Een ontbrekend pad, typefout, onbekende conversie, valuta-afwijking of niet-exacte geldconversie resulteert in geen credential.
- [ ] Nul resultaten levert de afzonderlijke uitkomst `no data`; meerdere resultaten leveren een fail-closed ambiguïteitsfout.
- [ ] Filters, sortering, `first`, joins, conditionals, scripts en externe I/O worden aantoonbaar geweigerd.

---

## Fase 3: Cache en GBO-gepubliceerde Type Metadata

**User stories:** US3, US6

### Wat te bouwen

Maak bronmetadata vernieuwbaar met JWS-verificatie, `ETag`, monotone bron- en typeversies en GBO-beheerde cachegrenzen. Laat de bestaande issuancestack iedere geaccepteerde Type Metadata-versie vóór activatie immutable publiceren, de SRI-hash berekenen en de resulterende `vct` plus `vct#integrity` bij de typeversie vastleggen.

### Acceptatiecriteria

- [ ] Cache-refresh gebruikt `ETag` en activeert nieuwe metadata pas nadat alle validatie en Type Metadata-publicatie zijn geslaagd.
- [ ] Een bron- of typeversie kan nooit met andere bytes worden overschreven en een versieterugval wordt geweigerd.
- [ ] GBO handhaaft minimale geldigheid, maximale refreshperiode en stale-grace onafhankelijk van de door de bron voorgestelde tijden.
- [ ] De publieke Type Metadata bevat dezelfde `vct` als het credential en wordt als `application/json` aangeboden.
- [ ] `vct#integrity` is door GBO berekend over de exact aangeboden bytes en wordt niet uit broninput gekopieerd.
- [ ] De issuance-server blokkeert ondertekening als `vct`, `vct#integrity` of typeversie niet overeenkomen.
- [ ] Een wallet/verifier kan Type Metadata ophalen en de integriteitswaarde succesvol controleren.
- [ ] Gewijzigde bytes op dezelfde versie-URL, een onbekende typeversie en verlopen cache buiten stale-grace worden fail-closed geweigerd.
- [ ] Oude Type Metadata-versies blijven bereikbaar zolang bestaande credentials geldig kunnen zijn.

---

## Fase 4: Lokale onboarding en certificaatprovisioning

**User stories:** US2, US7, US8

### Wat te bouwen

Introduceer een minimale bronregistratie en lokaal uitvoerbare onboardingcommands. De lokale certificaatprovider maakt ontwikkelcertificaten voor issuer, reader en status, slaat private keys uitsluitend in een genegeerde lokale secretlocatie op en activeert de bron pas na metadata-, trust- en certificaatcontrole. Deze fase bevat nog geen GitHub Actions.

### Acceptatiecriteria

- [ ] `validate-source` controleert een bronregistratie plus het ondertekende bronendpoint zonder permanente wijzigingen.
- [ ] `onboard-source --env local` is idempotent en voert validatie, lokale certificaatprovisioning, Type Metadata-publicatie en activatie uit.
- [ ] `--dry-run` maakt geen keys, certificaten, gepubliceerde typen of actieve bronregistratie aan.
- [ ] Private keys staan uitsluitend in een genegeerde lokale secretlocatie en komen niet in commandoutput of logs.
- [ ] Het issuercertificaat identificeert de onboarde bronhouder; reader- en statuscertificaten voldoen aan de eisen van de issuance-server.
- [ ] Een mislukte certificaat- of trustcontrole laat de bron inactief.
- [ ] Een nieuwe lokale checkout kan met gedocumenteerde commands de volledige inkomensverklaring uitgeven.

---

## Fase 5: Alle inkomensjaren zonder scope of usecasecatalogus

**User stories:** US1, US4, US9

### Wat te bouwen

Vervang de afzonderlijke inkomens-usecases door één brondefinitie met een runtimeparameter voor belastingjaar. Laat de PDP direct beslissen op bron, VCT, actor, opgeloste velden en concrete argumentwaarden. Verwijder voor deze bron de afhankelijkheid van `scope`, `flow` en adapter-usecaseconfiguratie.

### Acceptatiecriteria

- [ ] Eén brondefinitie ondersteunt de bestaande inkomensjaren zonder query- of mappingduplicatie in GBO.
- [ ] De PDP staat de afgesproken jaren 2024 en 2025 toe en weigert 2023 op basis van het opgeloste queryargument.
- [ ] Een query met één extra niet-toegestaan veld wordt geweigerd, ook wanneer bron, VCT, actor en jaar geldig zijn.
- [ ] Een onbekend argument, ontbrekend verplicht argument of typefout wordt vóór uitvoering geweigerd.
- [ ] De inkomensflow gebruikt geen `X-GBO-Scope`, `X-GBO-Flow` of adapter-usecasecatalogus meer.
- [ ] De bestaande walletflows voor alle drie de jaren houden aantoonbaar hetzelfde functionele resultaat.

---

## Fase 6: Tweede bron met domain-ready resolver

**User stories:** US1, US2, US5, US7, US9

### Wat te bouwen

Laat de BRP-testbron een eigen ondertekende bronmetadata en een domain-ready resolver voor de akte van overlijden aanbieden. De resolver selecteert de relevante relatie en overleden partner; daarna gebruikt GBO exact dezelfde generieke validatie-, policy-, mapping-, Type Metadata- en issuanceflow als bij inkomen.

### Acceptatiecriteria

- [ ] De BRP-bron wordt uitsluitend met een bronregistratie en lokaal onboardingcommand toegevoegd.
- [ ] De bronresolver retourneert exact één domeinresultaat of `no data` en bevat alle noodzakelijke domeinselectie.
- [ ] De adapter bevat geen BRP-profiel, partnerselectie of akte-specifieke projector.
- [ ] Een verkeerde actor, extra veld of niet-toegestane resolver/query wordt door de PDP geweigerd.
- [ ] De Type Metadata en het issuer-, reader- en statuscertificaat horen aantoonbaar bij de BRP-bronhouder.
- [ ] De lokale walletdemo geeft een correcte akte uit zonder bron-specifieke adapterwijziging.

---

## Fase 7: Lokale cutover, uitvaltesten en hardening

**User stories:** US3, US4, US6, US8, US9

### Wat te bouwen

Schakel lokaal volledig over op het generieke pad, verwijder de tijdelijke hardcoded fallback en maak fouten en auditinformatie operationeel zichtbaar. Test lokaal het uitlezen van een geldig FSC-contract via de bestaande mTLS Manager-API; behoud de gereviewde metadata-URL als fallback zolang productie- en CI-bereikbaarheid niet zijn vastgesteld.

### Acceptatiecriteria

- [ ] Alle hardcoded querybouw, bronprofielen, domeinprojecties en adapter-usecasecatalogus zijn verwijderd.
- [ ] Inkomensverklaring en akte van overlijden draaien lokaal volledig via bronmetadata.
- [ ] Metadata-uitval binnen en buiten stale-grace, ongeldige JWS, cachecorruptie, VCT-manipulatie, policy-denial en certificaatfouten zijn end-to-end getest.
- [ ] Audit en traces bevatten bron-OIN, type-id, typeversie, VCT, `vct#integrity`, bronmetadatadigest, querydigest, policybesluit en FSC-contract/grant indien beschikbaar.
- [ ] Het lokale onboardingcommand kan via mTLS een geldig FSC-contract en de juiste service client-side selecteren.
- [ ] Ontbrekende of onbereikbare contractinformatie volgt aantoonbaar de afgesproken fallback en opent nooit extra toegang.
- [ ] Een fresh-start lokale E2E-test bewijst beide bronnen zonder handmatige wijziging van query's of mappings in GBO.

---

## Fase 8: CI-automatisering en productieprovider

**User stories:** US2, US7, US8

### Wat te bouwen

Verplaats pas na de volledig werkende lokale E2E-flow dezelfde commands naar GitHub Actions. Gebruik een niet-geprivilegieerde PR-workflow voor validatie en een afzonderlijke, goedgekeurde post-mergeworkflow voor certificaatprovisioning, Type Metadata-publicatie en activatie. Koppel de productiecertificaatprovider pas nadat met het Wallet-team het CA- en trustmodel is vastgesteld.

### Acceptatiecriteria

- [ ] De PR-workflow roept exact dezelfde validator aan als lokaal en heeft geen toegang tot productiekeys of privileged secrets.
- [ ] Een ongeldige bronregistratie, JWS, query, mapping, Type Metadata of FSC-binding blokkeert merge.
- [ ] De post-mergeworkflow vereist environment approval en gebruikt korte OIDC-credentials of een gelijkwaardig mechanisme.
- [ ] De productieprovider genereert keys in HSM/KMS en ontvangt hooguit een CSR; private keys verschijnen nooit op de runner.
- [ ] Uitgifte, rotatie, intrekking en incidentherstel zijn voor issuer-, reader- en statuscertificaten getest en gedocumenteerd.
- [ ] Als FSC-contractcontrole netwerktoegang vereist, gebruikt de workflow een self-hosted runner binnen het GBO-netwerk; anders blijft de gereviewde URL de expliciete fallback.
- [ ] CI bevat unit-, conformance- en integratietests; een volledige wallet-E2E-test draait alleen in een daarvoor geschikte afgeschermde omgeving.
- [ ] Lokaal en CI produceren voor dezelfde broninput dezelfde gevalideerde bron- en Type Metadata-uitkomst.

## Eindresultaat

Na fase 7 is de volledige doelarchitectuur lokaal end-to-end aantoonbaar. Fase 8 automatiseert uitsluitend de reeds bewezen lokale commands en vervangt de ontwikkel-CA door de afgesproken productieprovider; CI introduceert geen eigen onboardinglogica.
