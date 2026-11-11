# eudi-wallet-verifier — the EUDI-wallet verifier of the ASI route

The OpenID4VP verifier with which the ASI authorization server has the citizen disclose the PID from an EUDI wallet (#639). It runs the verifier of `oid4vc-kit/oid4vp` behind the part of nl-wallet's `verification_server` API that the authorization server uses, so the server only gets a second verifier configured. The NL Wallet keeps `asi-wallet-verifier` until the NL Wallet v0.6 app works.

## What it does

- OpenID4VP 1.0 as HAIP 1.0 section 5 profiles it: a request object fetched by reference (GET), signed with the key of the usecase's access certificate (WRPAC), `client_id` its `x509_hash`; the registration certificate (WRPRC) in `verifier_info` as a JWT, as the EUDI wallets read it; DCQL with an `aki` `trusted_authorities` query; response mode `direct_post.jwt`.
- The query of a session stays within the usecase's registration: its PID type and claims. Without a query the session asks for all of them.
- The kit checks the PID: issuer chain against the configured anchors, key binding, the disclosed claims (no more and no fewer than asked), and the status list. A PID whose status is revoked or cannot be determined is refused.
- Same-device sessions of a usecase with `"return_url": "same_device"` return the wallet's browser to the relying party's return URL with the session token and a nonce. The disclosed attributes of such a session are released only with that nonce (HAIP 1.0 section 5.1). Cross-device sessions end at the verifier.
- Sessions are kept in memory.

## API

| Port | Endpoint | |
|---|---|---|
| 8001 | `GET /disclosure/sessions/{token}?session_type=cross_device\|same_device` | status; while `CREATED`, the `openid4vp://` link for that session type |
| 8001 | `GET /disclosure/sessions/{token}/request?session_type=…` | the signed request object, once |
| 8001 | `POST /disclosure/sessions/{token}/response` | the wallet's response; `{"redirect_uri": …}` for a same-device session with a return URL, else `{}` |
| 8002 | `POST /disclosure/sessions` | start a session: `usecase`, optional `dcql_query` and `return_url_template` (with `{session_token}`); `Authorization: Bearer <api-key>` |
| 8002 | `GET /disclosure/sessions/{token}/disclosed_attributes[?nonce=…]` | the disclosed claims of a `DONE` session; `401 {"type":"nonce"}` without the right nonce after a return |

Port 8001 must be reachable by the wallet over HTTPS: its public URL is in the configuration and names the request and response URIs.

## Configuration

`EUDI_WALLET_VERIFIER_CONFIG` (default `/config/config.json`); relative paths are resolved against its directory.

```json
{
  "public_url": "https://eudi-verifier.example",
  "api_key_file": "api-key",
  "session_ttl": "5m",
  "pid_trust_list": {
    "url": "https://trust.sim.example/lote/PIDProviders.jwt",
    "signer_root_file": "lote-signer-root.pem"
  },
  "usecases": {
    "asi-login": {
      "key_file": "asi-login.key.pem",
      "chain_file": "asi-login.chain.pem",
      "registration_certificate_file": "asi-login.wrprc.jwt",
      "pid_type": "urn:eudi:pid:nl:1",
      "claims": ["bsn", "family_name", "given_name", "birthdate", "nationalities"],
      "return_url": "same_device"
    }
  }
}
```

A usecase with `"return_url": "same_device"` requires a return URL template when a session starts; one with `"neither"` refuses it, as `verification_server` does.

The [central PID trust runbook](../../docs/gbo-trust-lists.md#internal-pid-consumers)
describes provisioning and the public signer pin. Trust refreshes on use at
most hourly; responses, including those of ongoing sessions, use current
roots. A refresh failure uses only an unexpired verified cache. With none,
the PID check returns `503 server_error` and leaves the session retryable.
An explicit `pid_trusted_authorities` filter overrides the derived AKI hints.
Legacy `pid_anchors` is an alternative configuration mode, mutually exclusive
with `pid_trust_list`, never an automatic fallback.
