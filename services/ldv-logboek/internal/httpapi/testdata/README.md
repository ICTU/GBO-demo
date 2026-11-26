# Logius schemas for the contract test

`logboek-extensie-lezen-openapi-1.1.0.json` and
`logboek-extensie-lezen-attributes.schema.json` are `media/openapi.json` and
`media/attributes.schema.json` of
[Logius-standaarden/logboek-extensie-lezen](https://github.com/Logius-standaarden/logboek-extensie-lezen)
(OpenAPI 1.1.0), copied unchanged. They are published under
[CC BY 4.0](https://creativecommons.org/licenses/by/4.0/) by Logius.

`fscread_contract_test.go` checks the reduced response of the FSC read listener
against the response schema in that file.
