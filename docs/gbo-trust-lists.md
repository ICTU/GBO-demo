# GBO simulation trust lists

The trust host exposes four lists in the JSON shape of ETSI TS 119 602
V1.1.1. They combine the NL Wallet **pre-production** configuration
with the GBO roots. They are simulation fixtures, not EU-notified lists.
See [the trust-host runbook](../trust-host/README.md) for commands and URLs.

| List | GBO anchor | NL anchors |
| --- | --- | --- |
| PIDProviders | GBO test-PID root | Explicitly fingerprinted NL PID issuer CA |
| PubEAAProviders | ca.gbo-issuer | Remaining `issuer_trust_anchors` |
| WRPACProviders | ca.gbo-reader | Legacy `disclosure.rp_trust_anchors`, plus `wrpac_trust_anchors` when present |
| WRPRCProviders | GBO development registrar root | `wrprc_trust_anchors` when present |

Both top-level and disclosure-scoped WRPAC/WRPRC fields are accepted for
the v0.6 fixtures. The generator does not synthesize missing NL registrar
anchors. SHA-256 over certificate DER controls deduplication: a GBO CA already
in the NL configuration appears once with both provenance URLs. Each entity
names its root and lists issuance and revocation verification services.
The PID, issuer, reader and registrar roles must remain distinct.

The reviewed NL PID CA fingerprint is
`6dace11d226dbedec67eb1792d56f722d93ed0ce9501004aa3fa22d9f38c5b38`,
subject `CN=issuance-ca.nonprod.wallet.edi.bzk.nl`. It was observed in
pre-production configuration version 49. This observation alone does not
authenticate that configuration. The agreed temporary demo mode trusts the
fixed official HTTPS endpoint; with an independently obtained configuration
signing key configured, JWT verification becomes mandatory.
`GBO_LOTE_NL_PID_CA_SHA256` accepts a comma-separated, explicitly reviewed
replacement or overlap set. A missing pin or a changed certificate under the
known PID subject refuses publication. The remaining NL issuer anchors are
classified as PubEAA by the agreed **demo policy**. That classification is
not evidence that every issuer is a public-sector body; a real scheme needs
authoritative role-specific approval information.

## Trust boundaries and shortcuts

- **Demo shortcut for the NL source:** without a configured key,
  `GBO_LOTE_TRUST_NL_HTTPS=true` trusts the fixed official pre-production URL
  through normally verified HTTPS. Redirects, different URLs and disabled TLS
  verification are rejected. The NL JWT signature is not checked in this mode.
  The risk is accepting changed content from a compromised official server.
  The signed legal notice, provenance, publication manifest and CLI output
  explicitly identify `https-only-demo`; our signature does not imply that we
  checked NL's signature. A real scheme must agree this source trust policy
  or independently provision and maintain the configuration-verification key.
- With a public key file configured, the NL configuration is verified as ES256
  and labelled `jwt-verified`. Errors never fall back to the demo shortcut.
  Both modes check `environment: preproductie`, a positive version and the
  required anchors. The generator does not take a verification key from the
  incoming JWT. Signed mode requires HTTPS for its configured URL and redirects.
- The independent LoTE scheme root signs only the list-signing certificate.
  This does not change any GBO credential, reader, registrar or test-PID root.
  The signer certificate's organization and country match the generated
  scheme operator (`GBO Demo`, `NL`).
- The JWS protected header contains `alg: ES256`, `typ: JOSE+JSON`, the
  signer certificate in `x5c`, its SHA-256 thumbprint, `kid` and `iat`.
  A valid signature under the certificate included in `x5c` proves possession
  of that certificate's key; it does **not** establish that a wallet trusts
  this scheme operator. Wallet trust bootstrap must be checked in C3 before
  the static configuration is removed.
- ETSI role/service identifiers are reused to exercise the reference wallets.
  The scheme territory and local policy identify the GBO simulation, and the
  signed legal notice disclaims EU notification and legal approval. Entity
  postal addresses are explicitly marked demo fixtures; they are not verified
  addresses of the NL issuers. Provenance remains the source configuration
  URL with its version and authentication mode, and/or the GBO policy URL.
- No revocation service for the LoTE signer is added. A real deployment needs
  independently distributed scheme trust and an agreed lifecycle/revocation
  policy. Existing CRL and registration-status routes remain available.
- The output is checked against the official ETSI JSON binding in tests.
  This is a structural check, **not** a claim of full TS 119 602 provider-profile
  or JAdES Baseline B conformance. The latter also references TS 119 182-1,
  which is not part of the local analysis inputs used for this change.
- Every fresh publication increments the sequence, including renewal with
  unchanged roots (TS 119 602 section 6.3.2). A no-op keeps the original issue
  and expiry times. An upstream outage never grants additional validity.

## Implementation boundary

`services/eudi-adapter/internal/trustlists` owns merging, role checks,
deterministic content identity, renewal and publication orchestration.
Its ports supply source material, signing and a serialized atomic update.
`nlconfig` enforces the declared trust mode and reads the remote configuration; `lotesigning` signs;
`lotefiles` reads public roots and activates filesystem generations.
`trust_lists.go` wires these adapters into the host CLI and provisioning.

The normative source is available in the analysis inputs as
`01-input/eudi/etsi/ts_119602v010101.txt`, with PDF page markers. The test-only
JSON binding is pinned to ETSI Forge revision
`e84f427f0cde99513b574ef4b5a155ac4a38eab6` and retains its license. It is also
stored under the analysis input's `forge/1960201/` directory. EU reference
lists supplied compatibility examples; their content is not imported into
GBO trust decisions.

## Internal PID consumers

The own credential issuer and the EUDI wallet verifier use the same central
`PIDProviders.jwt` for PID disclosure verification. The ASI and QTSP login
usecases share that EUDI verifier. Runtime configuration names one explicit
source:

```json
"pid_trust_list": {
  "url": "https://trust.sim.example/lote/PIDProviders.jwt",
  "signer_root_file": "lote-signer-root.pem"
}
```

The URL defaults to `EUDI_TRUST_PUBLIC_URL` plus `/lote/PIDProviders.jwt`;
`GBO_PID_TRUST_LIST_URL` can override it. The public signing root comes from
`GBO_LOTE_SIGNER_ROOT_FILE` on the provisioning host (Make defaults to the
public certificate projection's `development-ca/lote-ca-cert.pem`). Compose
mounts the public projection read-only into the materializer; provisioning
copies that pin into each consumer's runtime configuration. The scheme's
private keys never enter these containers. External consumers must likewise
obtain and trust this public signing root through an independent channel.
Fetching a list or its embedded `x5c` certificate does not establish trust.

`oid4vc-kit/lote` verifies ES256, the signer chain to that pin, current list
validity, the PID list role, and the PID issuance service before accepting CA
roots. It refuses a sequence rollback or changed content at the same sequence
within the process. The NL HTTPS-only shortcut applies to the generator's NL
input; it does **not** disable GBO list signature verification.

The two Go services refresh on the first trust check and on the first check
after one hour (earlier at cache expiry). Updates need no restart. Each PID
response obtains current roots, including responses to previously opened
sessions; removed roots no longer verify after refresh. The EUDI verifier
also derives its AKI discovery hints from the current roots, unless an
explicit `pid_trusted_authorities` filter is configured. The issuer preserves
its explicit per-profile AKI filters.

On a failed refresh the last verified snapshot is usable only until its
`NextUpdate` (or an earlier expiry in the signer's verified certificate chain). Failed refreshes
retry on use after one minute. With no valid snapshot, the PID request or
response receives `503 server_error`; a response is not consumed by that
outage. Metadata, cancellation and unrelated issuance flows remain available.
The demo cache is in memory: a restart needs a fresh successful fetch.
Static configuration remains available as an explicitly selected legacy
mode for separate deployments/tests. Configuring both static and LoTE trust
is rejected. A configured LoTE source never falls back to a PEM roots file.

The upstream **NL-wallet verifier** loads static roots at startup. The agreed
demo shortcut is to use `export-pid-trust` during
`make provision-asi-wallet-verifier`: it verifies the same central PID LoTE
and writes its exact PID roots as a provisioning snapshot. The provisioner
adds no general issuer CA. A failed fetch or verification aborts provisioning
without replacing the previous snapshot. Re-provision and recreate/restart the
NL verifier to apply list changes. **This upstream process does not enforce
LoTE `NextUpdate` while running**; an operator must refresh it. A deployment
requiring automatic removal and expiry for that process needs runtime LoTE
support or a supervised reload mechanism. This limitation does not apply to
the two Go consumers.

Publish and serve the signed lists at the configured HTTPS sim host before
provisioning the NL verifier or using the Go PID flows. Then run the existing
verifier provisioning targets and `make credential-issuer-up` for the own
issuer. The normal `demo-asi` recreation path applies changed verifier config;
a manual recreation must account for the NL verifier's single-file bind mount.
No URL is tied to the local review hostname.

This change covers PID issuer trust only. Wallet-provider and key-attestation
trust, TLS/FSC roots, and QTSP software-statement/seal trust serve different
roles and keep their explicit configuration. The four lists do not include a
WalletProviders role. PubEAA, WRPAC and WRPRC remain separate wallet-facing
lists; no component trusts all four roles indiscriminately.

Tests cover authenticated publisher
output through HTTPS, role/signature/expiry refusals, hourly refresh and
bounded outage cache, removal during sessions, temporary failures/recovery,
NL/EUDI disclosure profiles, exclusive runtime configuration, and the
startup-only NL verifier's exact snapshot. These are automated fixture tests;
they do not replace a deployment test.

## Wallet builds

Both wallet test builds use an explicit GBO demo profile, an independently
supplied LoTE signer root, NL PID classification and configurable endpoints.
They carry no static credential, reader or registrar roots and no pinned
access certificates.

See [wallet source patches and build commands](../wallet-patches/README.md) for
exact SDK revisions, the PubEAA mapping, signer checks, cache behavior and the
additional iOS shared-metadata-cache fix. SDK alpha.4 supplies CA path validation
without the full EU end-certificate profiles or PKIX revocation fetching; this
is an explicit demo shortcut. The default SDK anchor cache lasts one hour and
has weaker expiry/rollback semantics than the internal Go adapter.

The reproducible SDK preparation builds iOS ARM64 device and simulator artifacts
and runs the patched Android cache tests. Both app verifier implementations
pass signed positive and negative fixtures. Manual wallet acceptance tests are
not complete, and the renewal timer is an example that is not enabled.

No fixture signing key substitutes for the real NL pre-production public key;
the official-HTTPS shortcut for the NL source stays explicit in the published
provenance.
