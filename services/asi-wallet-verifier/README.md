# asi-wallet-verifier — the NL Wallet verifier of the ASI route

nl-wallet's `verification_server` (v0.5.0, `wallet_core/wallet_server/verification_server`): the OpenID4VP verifier with which the ASI authorization server has the citizen disclose the PID from the NL Wallet (slice 7 of #336). It is nl-wallet's own runtime, as D7 requires, built from the vendor checkout so it moves in lockstep with the wallet app.

The nl-wallet issuance server cannot take this role: it exposes only the wallet-facing routes (no session start, no disclosed attributes), and a disclosure there always ends in issuing an attestation.

## Build and run

- `make asi-wallet-verifier-image` builds `gbo/asi-wallet-verifier:dev` from `$NLWALLET_PATH` (default `vendor/nl-wallet`) with this `Dockerfile`, stamped with the nl-wallet revision, like `eudi-issuance-server`.
- `make provision-asi-wallet-verifier` writes `verification_server.toml` to `$(ONBOARDING_SECRETS_DIR)/asi-wallet-verifier`, and the API key of the internal API to `$(ONBOARDING_SECRETS_DIR)/asi-authorization-server-wallet/api-key`: the authorization server mounts only that directory, not the configuration with the reader's private key (command `provision-wallet-verifier` of the authorization server):
  - the reader certificate of usecase `asi-login`, issued by the development reader CA (`ca.gbo-reader`, which the test wallet trusts). The verifier's public host is its DNS SAN, and its reader authorisation allows `bsn`, `family_name`, `given_name`, `birthdate` and `nationalities` of `urn:eudi:pid:nl:1`;
  - the reader certificate of usecase `qtsp-login`, for the reference QTSP's login (slice 14 of #336), from the same CA: organisation "GBO demo test QTSP", and `family_name`, `given_name`, `birthdate` and `nationalities`, not the BSN. A demo shortcut: the QTSP shares this verifier and its API key with the authorization server; in production each relying party has its own verifier and keys;
  - trust in the development issuer CA and in the pre-production PID issuer CA (`pid-issuer-anchors.txt`);
  - sessions in memory, and the internal API behind a generated API key.
- `make demo-asi` builds and provisions it when the nl-wallet sources are present (again when the configuration lacks usecase `qtsp-login`), before it starts the authorization server and the reference QTSP, which read the API key at startup; after a new provisioning it recreates the ASI services. Without the sources, the ASI route runs with DigiD (mock) only.

## Ports

| Port | |
|---|---|
| 8001 (`GBO_PORT_ASI_WALLET_VERIFIER`, default 9428) | public: the wallet fetches the request and posts its response here, and the session status is read here |
| 8002 (compose network only) | internal: the authorization server and the reference QTSP start sessions and fetch the disclosed attributes, with `Authorization: Bearer <api-key>` |

A real wallet reaches the verifier only over HTTPS. Point a tunnel at the public port, set `ASI_WALLET_VERIFIER_PUBLIC_URL`, and run `make provision-asi-wallet-verifier` again: the reader certificate names the host.

## Known gaps

- The NL PID of nl-wallet v0.5.0 has no place of birth, although CIR (EU) 2024/2977 makes `birth_place` mandatory.
- The image is not in the published image manifest yet, like the reference QTSP and the DIP stub.
