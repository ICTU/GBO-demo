# GBO demo trust host

The nginx service publishes the reader CRL, wallet-registration status lists,
support pages and four role-specific Lists of Trusted Entities (LoTE).
It has no signing keys. Configure `EUDI_TRUST_PUBLIC_URL` for the simulation
deployment and `EUDI_TRUST_PUBLISH_DIR` for the public host directory.

| URL | Media type | Cache policy |
| --- | --- | --- |
| `/lote/PIDProviders.jwt` | `application/octet-stream` | `public, max-age=3600` |
| `/lote/PubEAAProviders.jwt` | `application/octet-stream` | `public, max-age=3600` |
| `/lote/WRPACProviders.jwt` | `application/octet-stream` | `public, max-age=3600` |
| `/lote/WRPRCProviders.jwt` | `application/octet-stream` | `public, max-age=3600` |
| `/reader-root.crl` | `application/pkix-crl` | `no-cache` |
| `/wrprc/1` | `application/statuslist+jwt` | `no-cache` |

The LoTE media type matches the EU reference host observed on 11 November
2026. The signed response is a compact JWS. These are demo trust lists:
ETSI role identifiers permit reference-wallet parsing; they do not assert EU
notification, public-sector status or legal approval.

## Provision and publish on the host

First provision the existing GBO roots, the registrar and the test-PID.
Their public projections must exist in
`ONBOARDING_PUBLIC_CERTIFICATES_DIR/development-ca/`:
`issuer-ca-cert.pem`, `reader-ca-cert.pem`, `registrar-ca-cert.pem` and
`test-pid-ca-cert.pem`. Publication reads these certificates, not their keys.

The agreed temporary **demo shortcut** trusts the fixed official endpoint
`https://static.preproductie.wallet.edi.bzk.nl/config/v1/wallet-config`
over HTTPS without verifying its JWT signature. Make enables this with
`GBO_LOTE_TRUST_NL_HTTPS=true`; the direct CLI requires `--trust-nl-https`
or that environment setting. Normal TLS certificate validation is mandatory,
and this mode rejects redirects and alternate URLs. It trusts content served
by that HTTPS endpoint, including content changed on a compromised server.
The signed lists, provenance, manifest and CLI output identify this mode as
`https-only-demo`.

When available, obtain the **public** NL Wallet pre-production configuration
verification key from a trusted build artifact or the NL Wallet team. Set
`GBO_LOTE_NL_CONFIG_KEY_FILE` to its host path. Accepted formats are a
`PUBLIC KEY` PEM, base64 SubjectPublicKeyInfo DER, or the pre-production
build's `config-server-config.json` (`environment: preproductie` and
`signing_public_key`). The public upstream `default-config-server-config.json`
belongs to **dev**. It cannot verify the pre-production configuration.
Once a key file is set, verification is mandatory even when the shortcut flag
remains enabled. An unreadable key or invalid signature fails the run; there
is no fallback to HTTPS-only trust. Set `GBO_LOTE_TRUST_NL_HTTPS=false` to
require a key even before one is configured. Verified mode is labelled
`jwt-verified` and may use a configured HTTPS URL.

```sh
make provision-trust-list-signer
make publish-trust-lists
```

The first command creates the dedicated scheme root once and keeps it on
later runs. It renews the signer when needed, keeping its key. Only
`lote-ca-cert.pem` is projected publicly. The root and signer keys stay under
`GBO_LOTE_SIGNER_DIR` (default `.local/lote-signer`), mode `0600` in a `0700`
directory. This directory is not mounted by this repository's compose file.
The command rejects placement inside the secrets or either public directory,
including symlink aliases. Deployment-specific mounts must also exclude it.

The generator fetches the NL configuration on **every run** and checks it
under the selected trust mode, including environment, version and role pins.
All four JWS files are prepared in one
generation and a relative `lote` symlink is replaced atomically. The trust
host mounts the parent public directory, so it sees later pointer swaps.
Concurrent runs are serialized by a host file lock. Internal manifests and
generations are not served. Existing generations remain on disk; do not
remove the one referenced by `lote`.

Missing roots, role conflicts, invalid NL signatures and fetch failures leave
the previous lists untouched. A lower NL version at the same source URL is
refused. Failed runs exit nonzero and do not extend the previous expiry.
Back up the publication directory with its generations and symlink; deleting
it loses the sequence history. A corrupt manifest is refused, not reset.

Unchanged input leaves the files, issue times and sequence unchanged.
Changed content, signer identity or provenance advances the shared sequence.
Validity defaults to 30 days. Within the last day it is renewed automatically
(within the last quarter of its validity for shorter lifetimes). Force a
renewal with new times and a higher sequence using:

```sh
make publish-trust-lists GBO_LOTE_PUBLISH_FLAGS=--renew
```

## Daily refresh in the simulation environment

`systemd/gbo-trust-lists.service.example` and
`systemd/gbo-trust-lists.timer.example` provide a daily **host-side** refresh.
Before installing them, set the service user and checkout path for that
simulation deployment. The checkout's `.env` supplies URLs and file paths
through Make. Keep the keys outside every container mount. Install the files
without `.example` and enable the timer after the trust-list interface review.
A failed run remains a failed host service run; the old generation stays
active. Neither the timer nor any simulation deployment is enabled by this
change.

## Review and verification

The rules, provenance and limitations are in
[`docs/gbo-trust-lists.md`](../docs/gbo-trust-lists.md).
The approved trust-list tests run with the adapter's normal tests. To also
exercise the actual nginx image and HTTP headers:

```sh
cd services/eudi-adapter
GBO_TEST_NGINX=1 go test . -run '^TestLoTETrustHost$' -v
```

That test uses an isolated temporary publication, a local port and the
already available `nginx:1.27-alpine` image. It does not restart the demo.
