# test-pid-source — bronpoort of the test PID

A test fixture for the own credential issuer (#614). It gives the issuer the
data of a test PID of the NL type (`urn:eudi:pid:nl:1`), so wallets without a
real PID, such as the EUDI reference wallet, can show the disclosure-based
route. It is no PID Provider and is only reachable inside the demo network.

`POST /attestations` with `{"subject": {"bsn": "..."}}` answers the issuer's
IssuableDocument contract: one PID document for a BSN in the BRP mock persons
(`services/brp-graphql-server/mockdata/personen.json`), an empty list for any
other BSN. Anything else is a 400.

The values take the forms of the NL Wallet's own PID issuer:

| PID claim | BRP mock |
|---|---|
| `bsn` | `bsn` |
| `given_name` | `voornamen` |
| `family_name` | `voorvoegsel` and `geslachtsnaam` |
| `birthdate` | `geboortedatum` |
| `age_over_18` | the date in Amsterdam; born on 29 February, 18 on 1 March in a common year |
| `nationalities` | descriptions of `heeftNationaliteit[].nationaliteit` (`Nederlandse`, `Belgische`, `Duitse`) |
| `address` | `woontOpBinnenland`: street, house number with letter and addition without spaces (`12B`), postal code without a space, place, country `Nederland`; left out for a person without an address in the Netherlands |

A person the stub cannot map (a duplicate BSN, an unreadable birthdate, an
unknown nationality) stops it at startup.

`recovery_code`, which the NL Wallet's own PID carries, is left out.

| Variable | |
|---|---|
| `PERSONS_FILE` | the BRP mock persons file (required) |
| `ADDR` | listen address, default `:8080` |
