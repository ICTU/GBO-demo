# test-pid-source

## Purpose

Test fixture of `eudi-credential-issuer` (`TEST_PID_SOURCE_URL`): one test PID
of type `urn:eudi:pid:nl:1` per person in
`services/brp-graphql-server/mockdata/personen.json`, as the issuer's
IssuableDocument contract (`POST /attestations`). No PID Provider, no published
port, never published as an image.

## Contracts

- It stops at startup on a person it cannot map, so a change to that mockdata
  file must keep every person mappable; `dev-portal-backend` lists the same
  persons.
- Claims take the NL Wallet PID issuer's forms ([README.md](README.md)); the
  issuer adds PID metadata and `iss`.

## Decisions

- Test PID from the BRP mock, for wallets without a real PID:
  [#645](https://github.com/ICTU/GBO-demo/pull/645),
  [#682](https://github.com/ICTU/GBO-demo/pull/682).
