# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Repository governance files (`CODE_OF_CONDUCT.md`, `publiccode.yml`, `CHANGELOG.md`) and a `.github/workflows/codeql-analysis.yml` workflow to comply with the [ICTU GitHub policy](https://github.com/ictu/github-policy).
- Repository-owner section in `README.md`.

### Changed
- `SECURITY.md` restructured with explicit *Current status*, *Supported versions*, and *Reporting a vulnerability* sections.
- Bron GraphQL-schema switched from the custom `inkomensgegevens` shape to the BD bronprofiel schema ([gbo-semantiek v0.3 `bd.graphql`](https://github.com/ICTU/gbo-semantiek/blob/main/v0.3/graphql/bd.graphql)): queries now go via `ingeschrevenPersoon(bsn)` → `heeftBelastingjaarAangifte` → `AangifteIH` with `Bedrag` amounts. The BD schema has no `belastingjaren` argument, so the dienstverlener-backend and EUDI-adapter filter/select the requested tax year(s) from the response. EUDI inkomensverklaring metadata updated accordingly (`peil_datum`/`grondslag_*`/`status_code` out, `indieningsdatum`/`status` in).

## [0.1.0] - 2026-07-20

### Added
- Initial import of the GBO reference-architecture demo (consent + wallet flows over OpenFSC v2.4.0).
