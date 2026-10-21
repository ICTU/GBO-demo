# Security considerations of the ASI authorization server

ETSI TS 119 478 clause 6.1.3.4 requires the authorization server of an ASIP to take into account RFC 6819, RFC 7636, RFC 9700 and the FAPI 2.0 Security Profile (REQ-AZSP-6.1.3.4-01). This page records how the demo's authorization server (`services/asi-authorization-server`) and the ASIP as its resource server (`services/asi-provider`) do so. Part of epic #336, slice 8.

**Basis.** The review follows RFC 9700 section 2 (the best practices) and the FAPI 2.0 Security Profile, Final, section 5. RFC 7636 is covered by the rows on PKCE. RFC 9700 updates the threat model of RFC 6819 and lists only what is new or changed; where RFC 6819 has a measure RFC 9700 does not repeat, it is named. ETSI cites these documents without a version; this review uses RFC 9700 (January 2025) and FAPI 2.0 Security Profile Final.

**Outcome per item:**

- **met**: the demo does it; the tests named prove it;
- **not applicable**: the feature does not exist in the demo;
- **not met, demo choice**: deliberately not done, with the reason;
- **not met, open**: not done, and how to deal with it is still open.

A test is named as `<package>:<TestName>`, with the package relative to `services/`.

## Redirect-based flows

| Item | Source | Outcome |
|---|---|---|
| Exact string matching of redirect URIs | RFC 9700 §2.1 | **met**. The redirect URI must be one of the client's registered URIs, character for character. `asi-authorization-server/internal/httpapi:TestAuthorizationRequestErrors` |
| No open redirector | RFC 9700 §2.1, §4.11; FAPI 2.0 §5.3.2.1 | **met**. An unknown client or redirect URI gets an error page, never a redirect; so does a choice for an unknown or expired transaction. `asi-authorization-server/internal/httpapi:TestUnknownClientOrRedirectURIIsNotRedirected`, `asi-authorization-server/internal/httpapi:TestChoiceThatCannotBeRedirected` |
| CSRF protection at the client | RFC 9700 §2.1, §4.7 | **met** through PKCE, which the server requires, and `state`, which the reference QTSP binds to its browser session. A client obligation; the server makes it possible |
| `iss` in the authorization response (RFC 9207), as a defence against mix-up | RFC 9700 §2.1, §4.4; FAPI 2.0 §5.3.2.2 | **not met, open**. The authorization response carries `code` and `state` only, and the metadata does not announce `authorization_response_iss_parameter_supported`. RFC 9700 requires a defence against mix-up from a client that talks to more than one authorization server, which a QTSP does when it verifies attributes at several ASIPs. ETSI TS 119 478 does not name RFC 9207 |
| No 307 redirect of a request with user credentials | RFC 9700 §2.1, §4.12; FAPI 2.0 §5.3.2.2 | **met**. The redirect to the client after the login is 302; internal steps use 303 |
| 303 for redirects | FAPI 2.0 §5.3.2.2 (should) | **not met, demo choice**. The redirect to the client is 302. After the DigiD mock's POST, browsers follow 302 with a GET, so the form is not sent again; 303 would make that explicit |
| Authorization responses only over encrypted connections; `http` redirect URIs only for native clients on loopback | RFC 9700 §2.6; FAPI 2.0 §5.3.2.2 | **not met, demo choice**. Registration accepts `https` redirect URIs and `http` on a loopback address. The reference QTSP is a web application on the developer's machine and uses loopback; RFC 8252 section 7.3 intends loopback for native apps. Locally only; a deployment in the simulation environment uses `https` |

## Authorization code and PKCE

| Item | Source | Outcome |
|---|---|---|
| Only the authorization code grant; no implicit grant, no resource owner password grant | RFC 9700 §2.1.2, §2.4; FAPI 2.0 §5.3.2.1, §5.3.2.2 | **met**. `response_type` must be `code` and `grant_type` must be `authorization_code`. `asi-authorization-server/internal/httpapi:TestAuthorizationRequestErrors`, `asi-authorization-server/internal/httpapi:TestGrantTypeMustBeAuthorizationCode` |
| PKCE required, S256 only | RFC 7636; RFC 9700 §2.1.1; FAPI 2.0 §5.3.2.2 | **met**. A request without an S256 challenge is refused; the verifier must have the form of RFC 7636 section 4.1 and match. `asi-authorization-server/internal/httpapi:TestAuthorizationRequestErrors`, `asi-authorization-server/internal/httpapi:TestWrongCodeVerifierIsInvalidGrant`, `asi-authorization-server/internal/authorization:TestPKCEMatches` |
| No PKCE downgrade | RFC 9700 §2.1.1, §4.8 | **met**: there is no request without a challenge, so no token request can present a verifier for one |
| PKCE support detectable | RFC 9700 §2.1.1 | **met**: `code_challenge_methods_supported` in the metadata. `asi-authorization-server/internal/httpapi:TestMetadata` |
| Codes used once, valid at most 60 seconds, bound to client and redirect URI | RFC 6749 §4.1.2; RFC 9700 §4.5; FAPI 2.0 §5.3.2.1, §5.3.2.2 | **met**. A code presented twice, by another client or with another redirect URI, is consumed and refused. `asi-authorization-server/internal/httpapi:TestCodeIsSingleUseShortLivedAndBoundToTheClient` |
| Pushed authorization requests (PAR) | FAPI 2.0 §5.3.2.2 | **not met, demo choice**. ETSI TS 119 478 asks to take FAPI 2.0 into account, not to apply it. A server that requires PAR refuses QTSPs that implement ETSI TS 119 478 only |
| Binding of the code to the DPoP key (`dpop_jkt`, RFC 9449 section 10) | FAPI 2.0 §5.3.2.1 | **not met, demo choice**. The code is bound to the client, its authentication and the PKCE verifier; the token issued for it is bound to the DPoP key of the token request |

## Clients and client authentication

| Item | Source | Outcome |
|---|---|---|
| Confidential clients only, authenticated with asymmetric keys | RFC 9700 §2.5; FAPI 2.0 §5.3.2.1 | **met**. `private_key_jwt` is the only method; client assertions are used once. `asi-authorization-server/internal/httpapi:TestClientAuthenticationIsPrivateKeyJWT`, `asi-authorization-server/internal/httpapi:TestReplayedClientAssertionIsInvalidClient` |
| Client assertion audience only the issuer, as a string | FAPI 2.0 §5.3.2.1 | **not met, demo choice**. The server also accepts the token endpoint and an array, as RFC 7523 section 3 allows. `asi-authorization-server/internal/httpapi:TestTokenEndpointAsAudienceIsAccepted` |
| Clock skew on client assertions | FAPI 2.0 §5.3.2.1 | **met**: 30 seconds, within the bounds of FAPI 2.0 (accept up to 10, refuse beyond 60) |
| Clients cannot choose their `client_id` or a claim that resembles a user | RFC 9700 §2.6, §4.15 | **met**. Registration generates the `client_id`; the client key is the key of the QTSP certificate; `sub` is an opaque value per token. `asi-authorization-server/internal/httpapi:TestRegistration`, `asi-authorization-server/internal/httpapi:TestSubIsOpaquePerToken` |
| Registration only for an entitled QTSP | ETSI TS 119 478 clause 6.1.3.2 | **met** against the stub register: a sealed software statement, a certificate that chains to a trust anchor, and the entitlement in the register; the entitlement is checked again periodically. `asi-authorization-server/internal/httpapi:TestUnapprovedSoftwareStatement`, `asi-authorization-server/internal/httpapi:TestRecheckDeactivatesAClientWithoutEntitlement` |
| Refresh tokens | RFC 9700 §2.2.2, §4.14; FAPI 2.0 §5.3.2.1 | **not applicable**: none are issued |

## Access tokens

| Item | Source | Outcome |
|---|---|---|
| Sender-constrained access tokens | RFC 9700 §2.2.1, §4.10; FAPI 2.0 §5.3.2.1 | **met**: every token is bound to the client's DPoP key; proofs are used once and accepted within 60 seconds of their `iat`. `asi-authorization-server/internal/httpapi:TestFullFlowIssuesDPoPBoundAccessToken`, `asi-authorization-server/internal/httpapi:TestTokenRequestNeedsAValidDPoPProof`, `asi-provider/internal/httpapi:TestReplayedDPoPProofIs401` |
| Audience restriction | RFC 9700 §2.3 | **met**: `resource` (RFC 8707) names one data service, which becomes the audience; the ASIP refuses a token for another. `asi-authorization-server/internal/httpapi:TestResourceIndicator`, `asi-provider/internal/httpapi:TestATokenIsForOneDataService` |
| Restriction to actions and resources | RFC 9700 §2.3; FAPI 2.0 §5.3.2.1 | **met**: the scope names the operations (`verify`, `retrieve`); with RFC 9396 `authorization_details` the token is also bound to the selected attributes. `asi-authorization-server/internal/httpapi:TestVerifyOnlyClientGetsVerify`, `asi-provider/internal/httpapi:TestInsufficientScopeIs401`, `asi-provider/internal/httpapi:TestTokenOnlyAuthorizesSelectedAttributes` |
| Short lifetime | RFC 6819 §5.1.5.3; FAPI 2.0 §6.1 | **met**: 10 minutes |
| Confidentiality of the token's content towards the client | RFC 9068 §6 | **not met, demo choice**. The token carries the user's BSN in plain text, and a QTSP may not process the BSN. Slice 9 of #336 protects it |
| Entropy of codes and identifiers | FAPI 2.0 §5.4.1; RFC 6749 §10.10 | **met**: 128 random bits |

## The ASIP as resource server

| Item | Source | Outcome |
|---|---|---|
| Token only in the `Authorization` header, never in the query | FAPI 2.0 §5.3.4 | **met**: the ASIP reads the token from the `Authorization` header only; a request without it is refused. `asi-provider/internal/httpapi:TestRequestWithoutTokenIs401` |
| Verification of signature, issuer, audience, expiry and DPoP binding | FAPI 2.0 §5.3.4; RFC 9068 §4 | **met**; the keys come from the metadata of the authorization server. `asi-provider/internal/httpapi:TestInvalidAccessTokenIs401`, `asi-provider/internal/httpapi:TestInvalidDPoPProofIs401`, `asi-provider/internal/httpapi:TestMissingDPoPProofIs401`, `asi-provider/internal/asmetadata:TestKeysFromAuthorizationServerMetadata` |
| Revocation status of the token | FAPI 2.0 §5.3.4 | **not met, open**. The ASIP checks the token itself and cannot learn that it was withdrawn. When the register withdraws a QTSP's entitlement, the authorization server deactivates the client, but tokens already issued remain valid until they expire, at most 10 minutes. ETSI TS 119 478 clause 6.1.3.2 asks for the re-verification, not for what it means for issued tokens |
| Errors | FAPI 2.0 §5.3.4; RFC 6750 §3.1 | **met**, with one interpretation: ETSI TS 119 478 lists 401 for every authentication or authorization error, so `insufficient_scope` is 401 where RFC 6750 uses 403. `asi-provider/internal/httpapi:TestInsufficientScopeIs401` |
| Algorithms | FAPI 2.0 §5.4.1 | **not met, demo choice**. FAPI 2.0 allows PS256, ES256 and EdDSA. The ASIP also accepts RS256, because RFC 9068 section 2.1 requires resource servers to support it; the authorization server signs with ES256 only and publishes EC keys only. `asi-provider/internal/httpapi:TestRS256AccessTokenIsAccepted` |

## Pages in the browser

| Item | Source | Outcome |
|---|---|---|
| No CORS at the authorization endpoint | RFC 9700 §2.6; FAPI 2.0 §5.2.3 | **met**: the server sends no CORS headers |
| Protection against clickjacking | RFC 9700 §4.16; RFC 6819 §4.4.1.9 | **met**: every page is sent with `X-Frame-Options: DENY` and `frame-ancestors 'none'`, and is not cached |
| The user decides with the necessary information | FAPI 2.0 §5.3.2.2 | **met**: the request page names the QTSP by its legal name in the register, the operation, the authentic source and, when the QTSP supplied them, the attributes. `asi-authorization-server/internal/httpapi:TestRequestPage`, `asi-authorization-server/internal/httpapi:TestDenyIsAccessDenied` |
| The login bound to the transaction and the browser | RFC 9700 §2.1.1 (binding to the user agent) | **met**: the transaction is bound to the browser by a cookie; a wallet session is kept with the transaction and never taken from the browser. `asi-authorization-server/internal/httpapi:TestWalletSessionIsBoundToItsTransaction` |

## Transport and cryptography

| Item | Source | Outcome |
|---|---|---|
| TLS on every endpoint, protection against TLS stripping (HSTS) | RFC 9700 §2.6; FAPI 2.0 §5.2 | **not met, demo choice** locally: the stack runs on `http://localhost`. A deployment in the simulation environment terminates TLS at its ingress and must set HSTS there |
| No `none` algorithm; key sizes | FAPI 2.0 §5.4.1 | **met**: the accepted algorithms are listed explicitly (ES256 and PS256 for client assertions, ES256 for DPoP proofs and tokens) |
| JWKS over TLS, no `x5u` or `jku` | FAPI 2.0 §5.4.2 | **met** for `jku` and `x5u`, which are not used; TLS as in the row above |

## Summary of what is not met

**Demo choices**, deliberate and documented:

- no PAR;
- no binding of the code to the DPoP key;
- the client assertion audience may also be the token endpoint;
- 302 instead of 303 to the client;
- `http` redirect URIs on loopback, locally only;
- RS256 accepted by the ASIP;
- the BSN in plain text in the token, until slice 9;
- no TLS locally.

**Open:**

- no `iss` in the authorization response (RFC 9207), and so no defence against mix-up for a QTSP that uses several ASIPs;
- tokens remain valid after a QTSP's entitlement is withdrawn, until they expire.

The other measures RFC 6819 names for authorization servers are covered by RFC 9700 or do not apply: there are no client secrets, refresh tokens or resource owner passwords.
