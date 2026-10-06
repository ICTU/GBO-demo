# Demo keys

The keys with which the demo's sources read the encrypted identity and the
encrypted pseudonym a consent token carries for them. The sidecar reads this
directory when `SUBJECT_KEYS_DIR` points at it and hands the keys to the
source's decryption component with every value.

| File | What it is |
|---|---|
| `ei-decryption-20260101.pem` | The key that reads an identity, for OIN `99999999900000000200` and key set version `20260101` |
| `ep-decryption-20260101.pem`, `ep-closing-20260101.pem` | The two keys that read a pseudonym, for the same OIN and key set version |
| `scheme-keys.json` | BSNk's public scheme keys, by URN: what a value is signed with |

They come from `bsnk-mock` (`POST /v2/provide-dv-keys` and
`GET /v2/scheme-keys`) and hold no key material: a key of the mock is its
headers, and its label is `BSNK MOCK DV KEY`, not that of a private key. Real
key files are secrets and never belong in this repository. A real source gets its key files from BSNk through a broker, keeps
them itself, and keeps the files of an earlier key set version for as long as
consent tokens made for it are valid.

The consent portal names the same OIN and key set version in `CONSENT_SOURCES`.
