# `gbo-simple-v1` mappingprofiel

Status: normatief voor bronmetadata met `mapping_profile: gbo-simple-v1`.

## Doel en uitvoermodel

Het profiel projecteert precies één door de GraphQL-query geselecteerd bronobject naar een vlak JSON-object met credentialclaims. De naam van een `mapping`-property is de doelclaim; `pointer` is een absolute RFC 6901 JSON Pointer relatief aan het geselecteerde bronobject. Daardoor zijn kopiëren, hernoemen en flattenen dezelfde operatie.

De uitvoering is fail-closed:

1. valideer de mapping volledig;
2. volg `result_pointer` vanaf de GraphQL-response;
3. pas cardinaliteit `exactly_one` toe;
4. lees en typecontroleer iedere claim zonder de waarde te veranderen;
5. geef alleen bij volledig succes het gehele claimobject terug.

Er wordt nooit een gedeeltelijk claimobject uitgegeven. Een lege resultatenlijst is de afzonderlijke uitkomst `no_data`; meer dan één resultaat is een fout.

## Toegestane regels

Iedere claimregel heeft exact deze vorm:

```json
{
  "pointer": "/persoon/geboortedatum",
  "datatype": "date"
}
```

Ook bedragen worden ongewijzigd gekopieerd:

```json
{
  "pointer": "/inkomen/waarde",
  "datatype": "number"
}
```

Een bronwaarde `43000.50` blijft dus `43000.50`; GBO rondt niet af en kiest geen schaal. Een eventuele eenheid zoals `EUR` is declaratieve semantiek in `attribute_schema` en Type Metadata. Als een productiebron een andere representatie nodig heeft, levert zijn domain-ready resolver die representatie aan. Conversies worden pas aan een nieuw profiel toegevoegd wanneer een productiecasus ze aantoonbaar nodig heeft.

## Datatypes

| `datatype` | Vereiste bronwaarde | Credentialwaarde |
| --- | --- | --- |
| `string` | JSON-string van maximaal 16 KiB | string |
| `boolean` | JSON-boolean | boolean |
| `integer` | exact geheel JSON-getal binnen int64 | dezelfde bronwaarde |
| `number` | eindig JSON-getal van maximaal 128 tekens | dezelfde bronwaarde |
| `date` | canonieke RFC 3339 full-date `YYYY-MM-DD` | string |
| `gYear` | geheel getal van 0 tot en met 9999 | integer |

`attribute_schema` moet voor iedere mappingclaim exact één overeenkomstige outputdefinitie bevatten. Het schema mag bij `type: number` een eenheid zoals `EUR` declareren; de projector interpreteert die eenheid niet.

## Grenzen

- 1–128 claims per mapping;
- claimnamen maximaal 64 tekens en patroon `[A-Za-z_][A-Za-z0-9_]*`;
- pointers maximaal 512 bytes en 32 segmenten;
- getallen maximaal 128 tekens en exponenten van -308 tot en met 308;
- JSON-array-indexen zijn canonieke niet-negatieve gehele getallen.

## Niet toegestaan

Er zijn geen conversies of operators voor filters, sortering, `first`, joins, conditionals, defaults, scripts, templates, expressies, netwerk- of andere externe I/O. Onbekende properties en datatypes worden geweigerd; ze worden nooit genegeerd. Domeinselectie, representatiekeuzes en afgeleide waarden horen in de resolver van de bron en moeten vóór projectie exact één domain-ready resultaat opleveren.

## Foutcodes

| Code | Betekenis |
| --- | --- |
| `GBO_SIMPLE_MAPPING_INVALID` | profiel, regel, datatype, pointer of grens is ongeldig |
| `GBO_SIMPLE_PATH_MISSING` | `result_pointer` of claimpointer bestaat niet |
| `GBO_SIMPLE_TYPE_MISMATCH` | bronwaarde voldoet niet aan het datatype |
| `GBO_SIMPLE_RESULT_TYPE` | `result_pointer` wijst niet naar een array |
| `GBO_SIMPLE_CARDINALITY_AMBIGUOUS` | `exactly_one` ontving meer dan één resultaat |

`no_data` is een uitkomst en geen technische foutcode.

## Machineleesbaar contract en conformance

- Mapping-JSON Schema: `schemas/gbo-simple-v1.schema.json` (Draft 2020-12).
- Envelope-schema: `schemas/gbo-attestations-v1.schema.json` verwijst naar dat profiel.
- Onafhankelijke fixtures: `services/eudi-adapter/internal/gbosimplev1/testdata/cases.json`.
- Uitvoeren: `cd services/eudi-adapter && go test ./internal/gbosimplev1`.
