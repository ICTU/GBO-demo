# GBO wallet LoTE demo profile

These source patches support the approved GBO simulation profile. They keep
`SchemeTerritory=NL`; they do not assert EU notification, certification, or
complete ETSI list/JAdES conformance. Nothing here publishes a dependency or
changes a simulation tenant repository.

The independent LoTE scheme root is supplied at build time. Credential issuer,
reader and registrar roots, and pinned end certificates, are obtained only from
the four signed GBO lists. There is no static fallback.

## SDK changes

| Patch | Upstream base | Change |
| --- | --- | --- |
| `etsi-gbo-profile.patch` | KMP ETSI `v0.4.0-alpha.4`, `b8a6cecd80f42a787f9066a27c5ef6cfcad06773` | Explicit CA-based PID, PubEAA, WRPAC and WRPRC metadata; Swift entry point; hourly Android file-cache refresh; verified cache replacement; bounded outage fallback |
| `android-sdk-gbo-profile.patch` | Android wallet core `v0.31.1`, `832be7f0c787ef1b720856ff5133400f9b720409` | Explicit `gboDemoProfile()` selection, mandatory custom verifier and authenticated disk cache; align AGP with the app |
| `ios-walletkit-cache.patch` | Existing GBO wallet-kit `0.54.5-gbo.1`, `4d269c2e52142190b203dc45328d7ffeb0e904e9` | Synchronize the shared issuer metadata cache; honor the existing cacheIssuerMetadata option |
| `ios-sdk-gbo-profile.patch` | iOS ISO security `v0.27.2`, `68ba43c3c62355797e3d3219aadd07a8962fdc06` | Explicit `.gboDemo` selection and custom loader; non-PID attestations use PubEAA; preserve profile when SDK copies context mappings |

The iOS app's existing wrapper used KMP alpha.3. This local profile builds
alpha.4 from source; the full application is compiled against its generated
headers. Public source remains upstream-licensed. Binary frameworks and local
Maven artifacts stay under `.local/` and must not be committed.

The default upstream verifiers verify the signature with the first `x5c`
certificate, without independently anchoring that certificate. The GBO app
verifiers therefore check ES256, a chain to the separately supplied scheme root,
certificate validity, the GBO operator and NL territory, list validity and
role-specific service identifiers. Signed metadata continues to use WRPAC;
WRPRC and PID remain separate contexts. `urn:eudi:pid:nl:1` is explicitly PID.

Sources inspected:

- [Android LoTE verifier](https://github.com/eu-digital-identity-wallet/eudi-lib-android-wallet-core/blob/v0.31.1/wallet-core/src/main/java/eu/europa/ec/eudi/wallet/trust/LoteJwtVerifier.kt)
- [iOS signature verifier](https://github.com/eu-digital-identity-wallet/eudi-lib-ios-iso18013-security/blob/v0.27.2/Sources/MdocSecurity18013/x509certificates/x5cVerifyJwtOrCwt.swift)
- [KMP file cache](https://github.com/eu-digital-identity-wallet/eudi-lib-kmp-etsi-1196x2/blob/v0.4.0-alpha.4/119602-consultation/src/commonMain/kotlin/eu/europa/ec/eudi/etsi119602/consultation/LoadSingleLoTEWithFileCache.kt)

The EU profile definition requires EU territory, but the investigated runtime
does not automatically call its `ensureCompliesToProfile` assertion. The GBO
profile makes the intended choices explicit and supplies the missing PubEAA
mapping; changing our published territory to EU is unnecessary.

## Prepare and build

On macOS with the existing Xcode and Android SDK installations:

```sh
python3 scripts/prepare-gbo-wallet-sdks.py --android --ios \
  --walletkit-source /path/to/existing/eudi-lib-ios-wallet-kit
```

This checks exact upstream revisions, applies the source patches without
resetting an existing checkout, tests the Android cache, and writes
`.local/dependencies/gbo-sdk-environment.json`. It never pushes or uploads.
The iOS artifact contains arm64 device and simulator slices. The existing wallet-kit fork is copied into a separate pinned local checkout before its cache patch is applied; the original fork is not changed.

Build each wallet with the public endpoints for that build variant and a
separately provisioned **public** scheme root:

```sh
python3 scripts/build-gbo-wallet.py ios \
  --wallet /path/to/eudi-app-ios-wallet-ui --variant dev \
  --trust-url https://trust.example.org \
  --issuer-url https://issuer.example.org \
  --root /path/to/independently-provisioned-lote-root.pem

python3 scripts/build-gbo-wallet.py android \
  --wallet /path/to/eudi-app-android-wallet-ui --variant dev \
  --trust-url https://trust.example.org \
  --issuer-url https://issuer.example.org \
  --root /path/to/independently-provisioned-lote-root.pem
```

For unpublished local iOS forks, also pass `--walletkit /path/to/fork` and
`--openid4vci /path/to/fork`. Their Git URL redirects apply only to that build
process, never global Git settings or credentials. Existing branding and PDF
signing dependencies are retained. Android's GBO configuration belongs to its
`dev` source set; iOS accepts dev/demo. The scripts build but do not install.

The Android fields can also be supplied as `gbo.dev.trustBaseUrl`,
`gbo.dev.issuerBaseUrl`, and `gbo.dev.loteRootBase64` Gradle properties.
The corresponding iOS build settings are `GBO_TRUST_BASE_URL`,
`GBO_ISSUER_BASE_URL`, and `GBO_LOTE_ROOT_BASE64`. Never download the root from
the list being authenticated as a substitute for independent provisioning.

Use normal simulator signing: the app needs its keychain and app-group
entitlements. An unsigned build can compile while being unable to access
wallet storage. Before updating a test installation, preserve its state and
check first-run preferences: upstream clears keychain documents on first run.
Never erase a device or change its PIN to work around this.

## Demo shortcuts and validation limits

The approved demo profile uses CA path validation without the full EU
end-certificate profiles or PKIX revocation fetching. Real deployment needs an
agreed certificate profile and operational revocation policy. Existing wallet
warning/enforcement choices remain: PID and signed issuer metadata are enforced;
non-PID/status warnings and the registration toggle retain their prior behavior.

Both SDK anchor caches use one hour. Android also has a patched disk cache;
iOS's verified JWT cache is memory-only. An HTTP/validation failure cannot
replace the last verified JWT, and a JWT reload rejects an expired list. The
underlying anchor cache may retain a previously resolved result until its
one-hour window ends; it does not persist a highest sequence across app restarts.
These wallet cache semantics differ from the stricter per-request expiry and
rollback controls in the internal Go adapter. Record actual wallet UI
behavior in a manual test; do not infer it from a successful build.

`fixtures/` contains public, deliberately artificial signer fixtures only.
Their private signing keys were discarded. Both real verifier implementations
are checked against valid lists, an unrelated root, wrong role/territory/algorithm,
modified signatures, malformed JWTs, future lists and expired lists. The fixed
test time is 11 November 2026. Cache tests cover hourly fetching, rejection of an
invalid replacement, HTTP 503 fallback and refusal after NextUpdate.


The iOS app changes live in the GBO fork of the iOS reference wallet,
[`jeroendk/eudi-app-ios-wallet-ui`](https://github.com/jeroendk/eudi-app-ios-wallet-ui),
branch `gbo/main`, commit `aae55fa3bc95ed3aec4b616834271c06776ada63`.

The Android app snapshot `android-app-lote.patch` is based on app commit
`4b295477f69351c1015ae253006bf240dd02d8ef` and includes the previously local
wallet-core 0.31.1 selection. It contains no dependency binaries or private
keys. Apply it only to a matching clean base, or review the diff against an
existing checkout; do not reset a user's work. Android local notes are
deliberately not copied into this snapshot.

The Android helper builds only ARM64 by default (228 MB instead of 469 MB).
Use `--android-abi x86_64` for an Intel emulator or `--android-abi all` for the
upstream multi-architecture output. Updating an existing emulator may still
require additional free space for Android's staging and dex optimization;
do not remove its existing wallet as an installation workaround.


The platform-independent Swift verifier check can also run directly on macOS:

```sh
swiftc /path/to/eudi-app-ios-wallet-ui/Modules/logic-core/Sources/Config/GboLoTEVerifier.swift \
  wallet-patches/verification/VerifierCheck.swift -o .local/verify-lote-swift
.local/verify-lote-swift wallet-patches/fixtures
```
