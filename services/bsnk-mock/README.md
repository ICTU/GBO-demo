# bsnk-mock

A stand-in for BSNk, the BSN-koppelregister that Logius runs.

It is a mock, not an implementation of BSNk. It follows BSNk's rules and its
interface, and has none of its cryptography: every value it issues can be read
by anyone. Use it with test BSNs only.

It serves two things.

| Part | Endpoints | Stands in for |
|---|---|---|
| BSNk | `/v2/…` | BSNk's own services |
| Decryption | `/signed-encrypted-identity`, `/signed-encrypted-pseudonym` | The decryption component a party runs itself |

In the demo the consent portal activates a citizen's BSN once, at their first
consent, and has the result transformed at every consent, through `/v2`: into
an encrypted pseudonym for every source, and into an encrypted identity for
each source on the BSN authorisation list. A source reads its value through the
decryption endpoints, on an instance of this image that serves nothing else
(`DECRYPTION_COMPONENT_ONLY`), so that the source does not call BSNk.

## How BSNk works

BSNk works with four values.

| Value | What it is | Who holds it |
|---|---|---|
| PI, polymorphic identity | Leads to the BSN | The party that requested it, and only that party can use it |
| PP, polymorphic pseudonym | Leads to a pseudonym | The same |
| VI, encrypted identity | Made for one party, which reads the BSN from it | That party |
| VP, encrypted pseudonym | Made for one party, which reads its own pseudonym for the citizen from it | That party |

A requester activates a BSN and gets a PI and a PP. For each party that needs
to know the citizen it then has a VI or a VP made. Each party reads its own
value itself, with keys it got from BSNk beforehand, and without calling BSNk.

## What a value looks like

A value has the fields of the real structure, under the real names. The real
one is binary and holds three curve points; this one is JSON and holds the BSN
or the pseudonym in plain sight. The transport form is the base64 of that
JSON.

A VI for a source, decoded:

```json
{
  "notationIdentifier": "2.16.528.1.1003.10.1.2.7.2",
  "signedEI": {
    "encryptedIdentity": {
      "notationIdentifier": "2.16.528.1.1003.10.1.2.1",
      "schemeVersion": "1",
      "schemeKeySetVersion": "1",
      "creator": "00000000000000000000",
      "recipient": "99999999900000000200",
      "recipientKeySetVersion": "20260101",
      "identityValue": "999991772"
    },
    "auditElement": "AAAAAAAAAAAAAAAAAAAAAA==",
    "issuanceDate": "20260101",
    "extraElements": [
      { "key": "RP:Nonce", "value": "550e8400e29b41d4a716446655440000" }
    ]
  },
  "signatureValue": {
    "signatureType": "0.4.0.127.0.7.1.1.4.4.3",
    "r": "7XJrh3L6Rw1++hmJfC3qZA==",
    "s": "XWDuBi956AZ8/F6z/GjFDQ=="
  }
}
```

Where the mock differs from the real structure:

| Value | Field | Holds |
|---|---|---|
| PI, VI | `identityValue` | The BSN |
| PP | `pseudonymSeed` | A one-way derivative of the BSN, so a PP cannot give the BSN back |
| VP | `pseudonymValue` | The pseudonym of this citizen at this party |

The real structures have `points` in that place. Everything else is as in the
real one: `recipient` is the OIN the value was made for, and
`recipientKeySetVersion` is the version of that party's keys.

A test can decode a value with any base64 library and assert on its fields.

## The BSNk endpoints

BSNk's own interface is SOAP. These endpoints carry the same requests and
answers as JSON, with the element and attribute names of the real messages as
field names. An unknown field in a request is an error.

Every request carries `RequestID`, `DateTime` and `Requester`, the OIN of the
caller. Every answer carries `ResponseID`, `DateTime` and `InResponseTo`.

### `POST /v2/activate`

Turns a BSN into a PI and a PP for the requester.

```json
{
  "RequestID": "_a1",
  "DateTime": "2026-09-30T10:00:00Z",
  "Requester": "99999999900000000900",
  "RequesterKeySetVersion": 1,
  "BSN": "999991772"
}
```

```json
{
  "ResponseID": "_5d1c…",
  "DateTime": "2026-09-30T10:00:00Z",
  "InResponseTo": "_a1",
  "PolymorphicPseudonym": ["<signed PI>", "<signed PP>"]
}
```

`GivenNames`, `SurName`, `DateOfBirth`, `DocumentType` and `DocumentID` are
accepted and not checked. `EncryptedBSN`, `EncryptedIdentity` and
`eIDAS-UniquenessID` are refused: the mock takes a plain BSN only.

### `POST /v2/transform`

Makes a value for each relying party.

```json
{
  "RequestID": "_t1",
  "DateTime": "2026-09-30T10:00:01Z",
  "Requester": "99999999900000000900",
  "PolymorphicIdentity": "<signed PI>",
  "PolymorphicPseudonym": "<signed PP>",
  "RelyingParty": [
    { "EntityID": "99999999900000000200", "KeySetVersion": 20260101, "IdentifierType": "Identity", "Nonce": "550e8400e29b41d4a716446655440000" },
    { "EntityID": "99999999900000000300", "KeySetVersion": 20260301, "IdentifierType": "Pseudonym" }
  ]
}
```

```json
{
  "ResponseID": "_9a02…",
  "DateTime": "2026-09-30T10:00:01Z",
  "InResponseTo": "_t1",
  "Encrypted": [
    { "EntityID": "99999999900000000200", "KeySetVersion": 20260101, "IdentifierType": "Identity", "value": "<signed VI>" },
    { "EntityID": "99999999900000000300", "KeySetVersion": 20260301, "IdentifierType": "Pseudonym", "value": "<signed VP>" }
  ]
}
```

The requester says per party what it gets. `PolymorphicIdentity` is needed
when a party asks for an identity, `PolymorphicPseudonym` when one asks for a
pseudonym.

A party occurs once in a request. A party that needs both an identity and a
pseudonym takes two requests: one for the identities, one for the pseudonyms.

`LinkVerification`, `Role` and `TransactionID` are refused.

### `POST /v2/provide-dv-keys`

Issues the keys with which a party reads its values. A broker asks for them on
the party's behalf; `Requester` is the broker.

```json
{
  "RequestID": "_k1",
  "DateTime": "2026-09-30T10:00:02Z",
  "Requester": "99999999900000000800",
  "RelyingParty": "99999999900000000200",
  "RelyingPartyPKIoCertificate": { "X509Data": { "X509Certificate": "<base64 DER>" } }
}
```

```json
{
  "ResponseID": "_c7e3…",
  "DateTime": "2026-09-30T10:00:02Z",
  "InResponseTo": "_k1",
  "EncryptedDVKey": [
    { "KeyType": "EP Decryption", "RecipientKeyVersion": "20260314", "SchemeKeySetVersion": "1", "value": "<base64 key file>" },
    { "KeyType": "EP Closing", "RecipientKeyVersion": "20260314", "SchemeKeySetVersion": "1", "value": "<base64 key file>" },
    { "KeyType": "EI Decryption", "RecipientKeyVersion": "20260314", "SchemeKeySetVersion": "1", "value": "<base64 key file>" }
  ]
}
```

The key set version is the date the certificate was issued. The certificate
must carry the party's OIN as the serial number of its subject.

A test without a certificate leaves it out and names the date itself:
`POST /v2/provide-dv-keys?key_set_version=20260101`.

A party gets the two keys for a pseudonym, and the key for an identity if it
may receive the BSN. `ProvideEIDecryptionKey` changes that:
`EI_DECRYPTION_KEY_EXPECTED` refuses the request when the party may not,
`WITHOUT_EI_DECRYPTION_KEY` leaves the key out.

A key file is a PEM block with the headers of a real key file. A real one is
labelled `EC PRIVATE KEY`; the mock's is labelled `BSNK MOCK DV KEY`, because
it holds no key and should not be taken for one:

```text
Recipient: 99999999900000000200
RecipientKeySetVersion: 20260314
SchemeKeySetVersion: 1
SchemeVersion: 1
Type: EI Decryption
```

It holds no key. For the mock, the headers are the key: the party, the key set
version and the kind. The real BSNk encrypts a key file to the party's
certificate; the mock only encodes it in base64.

### `GET /v2/bsn-authorisation-list`

Lists the parties that may receive the BSN. A requester looks a party up here
before it asks for an identity.

```json
{ "AuthorizedOrganization": [ { "OIN": "99999999900000000200" } ] }
```

### `GET /v2/scheme-keys`

Gives the scheme keys a party needs to verify its values, keyed by URN.

```json
{
  "urn:nl-gdi-eid:1.0:pp-key:mock:1:IP_P:1": "…",
  "urn:nl-gdi-eid:1.0:pp-key:mock:1:PP_P:1": "…"
}
```

With the real BSNk these are public keys. Here they are the secret of the
mock's signature, published like a public key. The signature tells a value the
mock issued from one that was altered or made up; it protects nothing.

### Faults

A refused request answers with BSNk's reason.

```json
{ "FaultReason": "ProvisioningRefused", "FaultDescription": "relying party 99999999900000000300 is not authorised to receive the BSN" }
```

| `FaultReason` | Status | When |
|---|---|---|
| `SyntaxError` | 400 | The request cannot be understood |
| `InvalidRequest` | 400 | A parameter of a key request is not acceptable |
| `AuthorizationError` | 403 | The requester may not do this |
| `ProvisioningRefused` | 403 | The request is understood and BSNk declines it |

## The decryption endpoints

A party reads its values itself, in a decryption component that it runs, with
its own keys. These two endpoints take the requests and give the answers of
that component, at the same paths, so that a caller written against them needs
no code change to use the real one. That has not been tried against the real
component. They are in this image only so that the demo needs one mock instead
of two. Start it with `DECRYPTION_COMPONENT_ONLY=true` and it serves these two
endpoints and nothing of BSNk, which is how the demo runs the component of its
sources.

### `POST /signed-encrypted-identity`

```json
{
  "signedEncryptedIdentity": "<signed VI>",
  "serviceProviderKeys": ["<key file>", "<key file>"],
  "schemeKeys": { "urn:nl-gdi-eid:1.0:pp-key:mock:1:IP_P:1": "…" }
}
```

```json
{
  "decodedInput": { "notationIdentifier": "2.16.528.1.1003.10.1.2.7.2", "signedEI": { "…": "…" }, "signatureValue": { "…": "…" } },
  "bsn": "999991772",
  "decryption_result": { "bytes": "AUIJOTk5OTkxNzcyAAAAAAAA", "version": "01", "type": "B", "length": 9, "identifier": "999991772" },
  "issuanceDate": "20260101",
  "extraElements": [ { "key": "RP:Nonce", "value": "550e8400e29b41d4a716446655440000" } ]
}
```

### `POST /signed-encrypted-pseudonym`

```json
{
  "signedEncryptedPseudonym": "<signed VP>",
  "serviceProviderKeys": ["<key file>", "<key file>"],
  "schemeKeys": { "urn:nl-gdi-eid:1.0:pp-key:mock:1:PP_P:1": "…" }
}
```

```json
{
  "decodedInput": { "notationIdentifier": "2.16.528.1.1003.10.1.2.8.2", "signedEP": { "…": "…" }, "signatureValue": { "…": "…" } },
  "pseudonym": "<the decoded pseudonym in transport form>",
  "decodedPseudonym": {
    "notationIdentifier": "2.16.528.1.1003.10.1.3.2",
    "schemeVersion": "1",
    "schemeKeySetVersion": "1",
    "recipient": "99999999900000000300",
    "recipientKeySetVersion": "20260301",
    "type": "B",
    "pseudonymValue": "…"
  },
  "issuanceDate": "20260101"
}
```

`targetClosingKey` is refused: the mock does not convert a pseudonym to
another closing key.

A failure answers `500` with the reason as plain text, as the real component
does.

## The rules the mock enforces

- **A PI or PP is for its requester only.** A transformation is refused when
  the requester is not the party the PI or PP was issued to.
- **An identity only for a party that may have the BSN.** Asking for an
  identity for a party that is not on the list is refused. Such a party can
  get a pseudonym. A party on the list can get either, or both in two
  requests.
- **At most four relying parties per transformation**, each once. If one
  party cannot be served the whole request fails and nothing is issued.
- **A value is for one party and one key set.** Reading it takes a key of the
  right kind for that recipient and that key set version. The keys of another
  party, or of another date, do not read it.
- **The key for a BSN only for a party that may have it.** Without it a party
  cannot read a VI, whoever handed it one.
- **A pseudonym is stable per party and key set, and differs otherwise.** The
  same citizen always has the same pseudonym at one party with one key set.
  Two parties cannot match their pseudonyms, and neither can get the BSN from
  its own. A party that gets new keys gets new pseudonyms.
- **A nonce is 8 to 128 letters and digits.** A UUID needs its hyphens
  removed.
- **A PI or PP cannot be read** by anyone.
- **An altered or made-up value is rejected.**

## Stable or randomised values

By default the same request gives the same value, so a test can compare
against a fixture. The issuance date is then fixed at `20260101`.

The real BSNk gives a different value on every request, even for the same
citizen and the same party. Code that compares two values, or uses one as a
key, passes against stable values and breaks against real ones. To catch that,
switch randomisation on: set `RANDOMIZE_VALUES=true` for the service, or add
`?randomize=true` to an activate or transform request. The values then differ
every time and still read as the same BSN or pseudonym. The issuance date is
then the first of the current month.

## What it leaves out

- No encryption: the BSN in a PI or VI is readable by whoever holds the value.
- No check that the requester is who it says it is; `Requester` is taken from
  the request.
- No check of the BSN or the verification data against a population register.
- No record of which key sets a party has; a transformation accepts any key
  set version.
- One scheme and one scheme key set.
- The variants the demo does not use: encrypted BSN input, link verification,
  direct encrypted values, pseudonym migration and closing key conversion.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `4003` | Listening port |
| `BSN_AUTHORISED_OINS` | empty | Comma-separated OINs that may receive the BSN. Empty means no party can get an identity |
| `RANDOMIZE_VALUES` | `false` | Whether values differ on every request |
| `DECRYPTION_COMPONENT_ONLY` | `false` | Serve the decryption endpoints alone, as the component a party runs itself |

## Logboek Dataverwerkingen

The consent portal records its call to BSNk as a Dataverwerking of its own
and sets `dpl.read.nextLogbookId` to this page (`LDV_BSNK_NEXT_LOGBOOK_ID`).
A source that reads its value calls nobody, so its record points nowhere.
The read extension asks for the read API of the party that was called; for a
party without one it allows a page with contact details instead. The mock
keeps no logbook, so this page is the honest answer to where its side of a
transform can be looked up.

In a real chain the value is whatever Logius publishes for BSNk: the read API
of its logbook, or its contact point until there is one.
