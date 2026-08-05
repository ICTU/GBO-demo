# `gbo-simple-v1` mappingprofiel

Status: normatief voor bronmetadata met `mapping_profile: gbo-simple-v1`.

## Doel en uitvoermodel

Het profiel projecteert precies één door de GraphQL-query geselecteerd bronobject naar een vlak JSON-object met credentialclaims. De naam van een `mapping`-property is de doelclaim; `pointer` is een absolute RFC 6901 JSON Pointer relatief aan het geselecteerde bronobject. Daardoor zijn kopiëren, hernoemen en flattenen dezelfde operatie.

De uitvoering is fail-closed:

1. valideer de mapping volledig;
2. volg `result_pointer` vanaf de GraphQL-response;
3. pas cardinaliteit `exactly_one` toe;
4. lees en converteer iedere claim;
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

De enige optionele bewerking is exacte geldschaling:

```json
{
  "pointer": "/inkomen/waarde",
  "datatype": "integer",
  "transform": {
    "operator": "money_scale",
    "currency_pointer": "/inkomen/valuta",
    "currency": "EUR",
    "source_scale": 2,
    "target_scale": 0
  }
}
```

`currency_pointer` is relatief aan hetzelfde geselecteerde bronobject. De waarde moet exact gelijk zijn aan de opgegeven ISO 4217-code. `source_scale` begrenst het aantal betekenisvolle decimalen in de bronwaarde. De projector vermenigvuldigt de decimale bronwaarde exact met `10^target_scale`; de uitkomst moet zonder afronding in een signed 64-bit integer passen. Zo is `43000.00 EUR` geldig voor schaal 0, maar `43000.50 EUR` niet.

## Datatypes

| `datatype` | Vereiste bronwaarde | Credentialwaarde |
| --- | --- | --- |
| `string` | JSON-string van maximaal 16 KiB | string |
| `boolean` | JSON-boolean | boolean |
| `integer` | exact geheel JSON-getal binnen int64 | integer |
| `number` | eindig JSON-getal van maximaal 128 tekens | getal, zonder precisieverlies |
| `date` | canonieke RFC 3339 full-date `YYYY-MM-DD` | string |
| `gYear` | geheel getal van 0 tot en met 9999 | integer |

Een `money_scale`-transform is alleen toegestaan bij `datatype: integer`. `attribute_schema` moet voor iedere mappingclaim exact één overeenkomstige outputdefinitie bevatten. Voor geld moeten `unit` en `scale` gelijk zijn aan `currency` en `target_scale` uit de transform.

## Grenzen

- 1–128 claims per mapping;
- claimnamen maximaal 64 tekens en patroon `[A-Za-z_][A-Za-z0-9_]*`;
- pointers maximaal 512 bytes en 32 segmenten;
- geldschalen 0–6;
- JSON-array-indexen zijn canonieke niet-negatieve gehele getallen.

## Niet toegestaan

Er zijn geen operators voor filters, sortering, `first`, joins, conditionals, defaults, scripts, templates, expressies, netwerk- of andere externe I/O. Onbekende properties, datatypes en operators worden geweigerd; ze worden nooit genegeerd. Domeinselectie en afgeleide waarden horen in de resolver van de bron en moeten vóór projectie exact één domain-ready resultaat opleveren.

## Foutcodes

| Code | Betekenis |
| --- | --- |
| `GBO_SIMPLE_MAPPING_INVALID` | profiel, regel, datatype, pointer of grens is ongeldig |
| `GBO_SIMPLE_PATH_MISSING` | `result_pointer`, claimpointer of valutapointer bestaat niet |
| `GBO_SIMPLE_TYPE_MISMATCH` | bronwaarde voldoet niet aan het datatype |
| `GBO_SIMPLE_CONVERSION_UNSUPPORTED` | operator of conversie is onbekend |
| `GBO_SIMPLE_CURRENCY_MISMATCH` | bronvaluta is afwezig, verkeerd getypeerd of niet gelijk |
| `GBO_SIMPLE_MONEY_INEXACT` | schaal of conversie vereist afronding of past niet in int64 |
| `GBO_SIMPLE_RESULT_TYPE` | `result_pointer` wijst niet naar een array |
| `GBO_SIMPLE_CARDINALITY_AMBIGUOUS` | `exactly_one` ontving meer dan één resultaat |

`no_data` is een uitkomst en geen technische foutcode.

## Machineleesbaar contract en conformance

- Mapping-JSON Schema: `schemas/gbo-simple-v1.schema.json` (Draft 2020-12).
- Envelope-schema: `schemas/gbo-attestations-v1.schema.json` verwijst naar dat profiel.
- Onafhankelijke fixtures: `services/eudi-adapter/internal/gbosimplev1/testdata/cases.json`.
- Uitvoeren: `cd services/eudi-adapter && go test ./internal/gbosimplev1`.
