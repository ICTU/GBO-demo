# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Akte van overlijden — a second bron and a second EUDI attestation.** New source service `brp-graphql-server` serving the BRP bronprofiel ([gbo-semantiek v0.3 `brp.graphql`](https://github.com/ICTU/gbo-semantiek/blob/main/v0.3/graphql/brp.graphql)): `IngeschrevenPersoon`/`Ingezetene`/`NietIngezetene`, nationaliteit, huwelijk, verblijfstitel, gezagsverhouding, immigratie and the binnen-/buitenlandse verblijfadres. A nabestaande discloses her PID and receives an `nl.gbo.brp.akte-van-overlijden` credential with her deceased partner's overlijdensgegevens; the query is rooted at her own persoonslijst and reaches the overledene only through `heeftHuwelijk.partners`, which is the authorization argument the policy mirrors. Mock data holds exactly one person who satisfies that shape (Frouke Jansen, BSN `999991772`, the PID-preprod persona); the other personen are married to a living partner, unmarried, or divorced.
  - Registered as its own FSC service `brp` (outway path `/brp`) behind the same provider peer as the BD bron, with its own `brp-sidecar` instance. `seed-bri-contract.sh` is now parameterised by `SERVICE_NAME`/`SERVICE_ENDPOINT_URL`/`GRANT_LINK_PATH`; `make fsc-seed-brp` seeds the new service.
  - New policy rule `EUD0002` with scope `brp:akte:overlijden`. Its `covers_fields` are disjoint from `EUD0001`'s, so a BD scope cannot open the BRP path and vice versa; the BD path's per-year axis is declared off.
  - The PDP selects the mirror schema per flow (`flowSchemaFiles`): `eudi:attestation` → BD, `eudi:attestation:brp` → BRP. Both bronprofielen expose `Query.ingeschrevenPersoon(bsn)` with a different `IngeschrevenPersoon`, so the flow name has to carry the bronprofiel.
  - The eudi-adapter dispatches on a per-usecase `bron` (`bd`/`brp`), which drives both the GraphQL query it builds and the wallet-attribute mapping.
  - The credential carries what a Dutch akte van overlijden carries and nothing more: the overledene (naam, geboortedatum/-plaats/-land, geslacht), the overlijden (datum, plaats, land), the ouders, the echtgenoot or geregistreerd partner, and the verklaring. The query fetches more — `datumVoltrekking`/`plaatsVoltrekking`/`landVoltrekking`/`datumOntbinding`/`redenOntbinding` select the right marriage and establish that it ended in death — but those belong on the huwelijksakte and are dropped, as are the BSN and the persoonslijst-ids. Three fields a paper akte does carry are absent because the bronprofiel has no equivalent: gemeente van opmaak + aktenummer, the aangever, and the tijdstip van overlijden.
- Repository governance files (`CODE_OF_CONDUCT.md`, `publiccode.yml`, `CHANGELOG.md`) and a `.github/workflows/codeql-analysis.yml` workflow to comply with the [ICTU GitHub policy](https://github.com/ictu/github-policy).
- Repository-owner section in `README.md`.

### Changed
- `SECURITY.md` restructured with explicit *Current status*, *Supported versions*, and *Reporting a vulnerability* sections.
- Bron GraphQL-schema switched from the custom `inkomensgegevens` shape to the BD bronprofiel schema ([gbo-semantiek v0.3 `bd.graphql`](https://github.com/ICTU/gbo-semantiek/blob/main/v0.3/graphql/bd.graphql)): queries now go via `ingeschrevenPersoon(bsn)` → `heeftBelastingjaarAangifte` → `AangifteIH` with `Bedrag` amounts. EUDI inkomensverklaring metadata updated accordingly (`peil_datum`/`grondslag_*`/`status_code` out, `indieningsdatum`/`status` in).

### Added
- Per-year policy enforcement: `heeftBelastingjaarAangifte` accepts a `belastingjaren` filter (demo-bron extension of the upstream schema) so the PDP can see the requested years. New `years_in_scopes` rule axis requires every requested belastingjaar to be covered by a `bd:ib:<year>` scope in the consent (DVT0001) or the rule's `allowed_scopes` (EUD0001) — a query for a year the citizen did not consent to fails with `YEAR_NOT_COVERED`; a missing year filter fails closed.
- Flow dispatch in the GBO rule-engine: consent-based rules (DVT0001) only fire when `pip.consent` is present, PID-based rules (EUD0001) only when `pip.pid` is present, so deny reasons come from the flow's own rule (implements the dispatch the rule files already documented).
- PDP by-PI consent lookup now unions all ACTIVE consents for the PI (per-year scopes may live in separate records; broadening consent over time works).
- Compose host ports are configurable via `GBO_PORT_*` env vars (defaults unchanged), so two worktree stacks can run side by side.
- DvTP requests now carry the referenced consent (`X-GBO-Consent-Id` header through the FSC path); the PDP's PIP lookup evaluates exactly that record (the by-PI union is only a legacy fallback), so revoking a consent deterministically denies queries backed by it and sibling consents for the same PI no longer rescue a query.
- fsc-infra is deployable per worktree: `FSC_PROJECT_NAME` + `FSC_PORT_*` in `fsc-infra/.env` (project name, network, host ports) and `FSC_INFRA_NETWORK` in the root `.env` let multiple checkouts run isolated FSC instances side by side.
- Dev-portal scenario `use-jaar-niet-geconsenteerd-deny` demonstrating per-year consent.
- Demo policies (Rego) and GraphQL mirror-schemas are now baked into the `opa` and `pdp-service` images (`services/opa/Dockerfile`, `services/pdp-service/Dockerfile`, build context = repo root). The compose stack and the Helm example values no longer mount them; a volume mount at `/policies` or `/schemas` still shadows the baked-in files if present.
- DvTP browser flow: the dienstverlener-backend intersects requested belastingjaren with the consent's scopes and only queries consented years; unconsented years are returned as `denied_years` and rendered greyed out in the dienstverlener-mock result page instead of failing the whole query.

## [0.1.0] - 2026-07-20

### Added
- Initial import of the GBO reference-architecture demo (consent + wallet flows over OpenFSC v2.4.0).
