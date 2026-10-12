# asi-authorization-server — authorization server of the ASIP mock (ETSI TS 119 478 clause 6.1.3)

The authorization server (I4 Authorize) of the ASIP mock in [`services/asi-provider`](../asi-provider). A qualified trust service provider (QTSP) obtains a DPoP-bound JWT access token here and presents it to the ASIP for `/verify` or `/retrieve`. Part of epic #336; this is slice 2 (#503).

The ASIP and the authorization server may be operated by one party, but they are two roles (ASIP and AZSP). They are separate services with separate keys: the ASIP only knows the issuer and fetches the signing keys through the metadata.

## What is real and what is not

| Part | Status |
|---|---|
| Authorization code flow (RFC 6749 section 4.1) with PKCE, S256 only (REQ-AZSP-6.1.3.1-01, -04) | implemented |
| Client authentication with `private_key_jwt` (REQ-AZSP-6.1.3.1-05) | implemented; the only method offered |
| DPoP-bound access tokens (REQ-AZSP-6.1.3.1-06) | implemented; every token is bound |
| JWT access tokens per RFC 9068 (REQ-AZSP-6.1.3.1-08) | implemented; contents below |
| Metadata (RFC 8414) and JWKS | implemented |
| Client registration | **in configuration only**; dynamic client registration with a sealed software statement (REQ-AZSP-6.1.3.2-01) is the second pull request of #503 |
| User identification (REQ-AZSP-6.1.3.1-02) | **stub, not conformant**: the client names a test person in `login_hint` and no user is involved. Slice 3 replaces it with a test-person chooser, slice 7 with OpenID4VP PID disclosure |
| mTLS, PAR, refresh tokens | not offered (see below) |

## Endpoints

| Endpoint | |
|---|---|
| `GET /.well-known/oauth-authorization-server` | metadata (RFC 8414) |
| `GET /jwks.json` | the public token signing key |
| `GET /authorize` | authorization request: `response_type=code`, `client_id`, `redirect_uri`, `scope` (`verify`, `retrieve` or both), `state`, `code_challenge`, `code_challenge_method=S256`, optionally `resource`, and `login_hint` for the stub |
| `POST /token` | token request: `grant_type=authorization_code`, `code`, `redirect_uri`, `code_verifier`, `client_assertion_type`, `client_assertion`, and a `DPoP` proof header |

## Run

```bash
cd services/asi-authorization-server
AZSP_CLIENTS_FILE=clients.json go run .
```

Configuration: `AZSP_ADDR` (default `:4021`), `AZSP_ISSUER` (the public base URL, default `http://localhost:4021`), `AZSP_RESOURCE` (the ASIP base URL, default `http://localhost:4020/authsrc-api`), `AZSP_SIGNING_KEY_FILE` (PEM, ECDSA P-256; without it the server generates a key at start, so tokens do not survive a restart), `AZSP_CLIENTS_FILE`, `AZSP_TEST_PERSONS_FILE` (default `testdata/test-persons.json`).

A clients file registers each client with its redirect URIs, its scopes and the public keys of its client assertions:

```json
{
  "clients": [
    {
      "client_id": "test-qtsp",
      "redirect_uris": ["https://qtsp.example/callback"],
      "scope": "verify retrieve",
      "jwks": { "keys": [ { "kty": "EC", "crv": "P-256", "x": "…", "y": "…", "kid": "test-qtsp-key" } ] }
    }
  ]
}
```

There is no client to try the flow by hand yet; the reference QTSP client is slice 6 of #336. `e2e_test.go` runs the whole flow against a started `asi-provider`.

## The access token

A JWT according to RFC 9068, signed with ES256, `typ` `at+jwt`, with:

| Claim | |
|---|---|
| `iss`, `aud`, `exp`, `iat`, `jti`, `client_id` | RFC 9068 section 2.2; `aud` is the ASIP base URL, the lifetime is 10 minutes |
| `sub` | an opaque value per token, so two tokens for one user cannot be linked through it (D3) |
| `scope` | `verify`, `retrieve` or both: the operations ETSI TS 119 478 authorizes (clause 6.1.3.2) |
| `cnf.jkt` | the thumbprint of the client's DPoP key (RFC 9449 section 6) |
| `https://gbo.example/claims/bsn` | the BSN of the user, **in plain text** (D3). The BSN has no entry in the IANA JWT registry, so the claim name is collision-resistant (RFC 9068 section 2.2.2); the namespace is provisional (D8). Slice 9 of #336 protects it |

The PID attributes (family name, given names, birth date, birth place, nationality) follow in slice 3.

## Interpretations and choices

- **One mechanism of each.** REQ-AZSP-6.1.3.1-05 and -06 require mTLS or `private_key_jwt`, and mTLS or DPoP. This server offers `private_key_jwt` and DPoP. A production deployment should consider mTLS.
- **No PAR.** The FAPI 2.0 Security Profile requires pushed authorization requests; ETSI TS 119 478 only asks to "take into account" FAPI 2.0 (REQ-AZSP-6.1.3.4-01). A stricter server than ETSI would exclude QTSPs that implement ETSI only.
- **Algorithms.** Tokens are signed with ES256. Client assertions may use ES256 or PS256, DPoP proofs ES256. RFC 9068 section 2.1 requires resource servers to support RS256, which FAPI 2.0 does not allow; the ASIP accepts it, this server never uses it.
- **Authorization per operation**, not per attribute: ETSI TS 119 478 defines the scopes `verify` and `retrieve` and no binding of a token to attributes.
- **Resource indicator.** `resource` (RFC 8707) is optional; the only accepted value is the ASIP base URL, any other value is `invalid_target`.
- **Codes** are single-use and valid for 60 seconds; a code presented by another client or with another `redirect_uri` is consumed and refused.
- **Clock leeway**: 30 seconds on client assertions; DPoP proofs are accepted within 60 seconds of their `iat` and only once.
- No log line contains the BSN.
