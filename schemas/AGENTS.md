# schemas

## Purpose

Contracts between the sources, onboarding and the PDP, each read from outside:

| File                                               | Read by                                                                         |
| -------------------------------------------------- | ------------------------------------------------------------------------------- |
| `gbo-source-metadata-v1.schema.json`               | the reconciler (`eudi-adapter reconcile-sources --schema`)                      |
| `gbo-simple-v1.schema.json`, `asi-attributes.json` | the reconciler, next to `--schema`; the catalogue also the `asi-provider` image |
| `pdp-mirror/`                                      | `openftv-pdp`, read once at its first request                                   |

## Contracts

- The three JSON files share one directory, also in the Helm chart's ConfigMap.
  The source documents in `services/*graphql-server/config/` must stay valid
  against them.
- `asi-attributes.json` holds placeholder identifiers until the EU catalogue of
  attributes provides the schemas; `asimapping.ParseCatalogue` allows only
  `type`, `properties`, `required`, `items` and `format`.
- Every element of a `pdp-mirror/*.graphql` must match the running source, which
  may have more. Type names must be unique across the files: rules bind on
  `Type.field`.

## Patterns

Change a source's GraphQL schema: `buildSchema` in the source, its `pdp-mirror`
file, `scripts/gen-graphql-pins.sh` (rewrites
`policies/gbo/graphql_schemas.rego`), the rules that name the fields, and the
query in the source's document if it uses them. A new FSC service also goes in
`services.json` and `scripts/check-schema-drift.sh`.

## Verify

`scripts/check-schema-drift.sh` and `make policy-test`; after a JSON schema
change `go test ./...` in `services/asi-mapping`, `services/eudi-adapter` and
`services/asi-provider`.

## Decisions

- Metadata `1.0` changed in place while there were no outside sources:
  [#463](https://github.com/ICTU/GBO-demo/pull/463); `1.1` adds `asi`:
  [#611](https://github.com/ICTU/GBO-demo/pull/611).
- One schema per FSC service, checked against the running sources:
  [#482](https://github.com/ICTU/GBO-demo/pull/482),
  [#495](https://github.com/ICTU/GBO-demo/pull/495).

## Links

- [../docs/source-onboarding.md](../docs/source-onboarding.md) and
  [../docs/gbo-simple-v1.md](../docs/gbo-simple-v1.md) (Dutch): the document and
  mapping rules; open before changing those schemas.
