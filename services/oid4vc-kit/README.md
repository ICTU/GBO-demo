# oid4vc-kit — OpenID4VC building blocks (Go library)

Protocol building blocks for services that issue or verify credentials for EUDI wallets. The protocol packages hold no state and do no I/O; callers keep replay state, nonces and keys. The separate `lote` adapter fetches and caches signed GBO PID trust lists. Used by `eudi-credential-issuer`, `eudi-wallet-verifier` and host provisioning.

| Package | |
|---|---|
| `lote` | HTTPS adapter for the GBO role-specific LoTE lists; independently pinned signer, hourly in-use refresh and cache bounded by signed expiry |
| `x5c` | verify an `x5c` chain against named trust anchor sets, with an optional required EKU |
| `dpop` | verify DPoP proofs (RFC 9449), including `ath` |
| `clientattest` | verify a client attestation and its PoP (draft-ietf-oauth-attestation-based-client-auth) |
| `keyattest` | verify key attestations in JWT format (OpenID4VCI 1.0 Appendix D.1) |
| `oid4vci` | OpenID4VCI 1.0 messages, `jwt` key proof verification (Appendix F.1) and encrypted credential requests and responses (section 10; ECDH-ES, A128GCM/A256GCM) |
| `sdjwtvc` | issue and parse SD-JWT VC (`dc+sd-jwt`) |
| `statuslist` | encode, decode and sign Token Status Lists (one bit per entry) |
| `testpki`, `testwallet` | for tests only: throwaway certificate chains and the messages a wallet and its provider send |

Only ES256 is supported. Dependencies are the standard library and `go-jose`.
