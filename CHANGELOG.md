# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
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
- Dev-portal scenario `use-jaar-niet-geconsenteerd-deny` demonstrating per-year consent.
- Demo policies (Rego) and GraphQL mirror-schemas are now baked into the `opa` and `pdp-service` images (`services/opa/Dockerfile`, `services/pdp-service/Dockerfile`, build context = repo root). The compose stack and the Helm example values no longer mount them; a volume mount at `/policies` or `/schemas` still shadows the baked-in files if present.
- DvTP browser flow: the dienstverlener-backend intersects requested belastingjaren with the consent's scopes and only queries consented years; unconsented years are returned as `denied_years` and rendered greyed out in the dienstverlener-mock result page instead of failing the whole query.

## [0.1.0] - 2026-07-20

### Added
- Initial import of the GBO reference-architecture demo (consent + wallet flows over OpenFSC v2.4.0).
