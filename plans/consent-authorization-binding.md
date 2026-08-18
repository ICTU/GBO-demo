# Plan: Consent authorization binding

> Source: https://github.com/pjay-io/gbo-analysis/issues/113
>
> Implementation status: completed on `jeroen/113-consent-authorization-binding`.
> The checklist below is retained as the regression contract.

## Architectural decisions

- **Authorization artifact**: S01 issues an ES256 JWT with `typ=gbo-consent+jwt` and a `kid`. It binds `consent_id`, PI, scopes, field selections, `dienstverlener_oin`, `iss`, `aud`, `iat`, `nbf`, `exp`, and `jti` in one signed object.
- **Trust boundary**: the PDP accepts the consent token only in the DvTP flow, verifies its signature and registered claims, and compares `dienstverlener_oin` with the caller OIN supplied by the validated FSC context.
- **Subject binding**: the PI in the GraphQL subject argument must equal the signed PI. Neither request headers nor an online lookup may replace signed authorization claims.
- **Revocation**: S01 remains authoritative for status through `GET /consents/<id>/status`; the PDP checks this endpoint for every request and fails closed.
- **Persistent schema**: S01 does not persist PI. It stores a consent-portal-specific pseudonym as `subject_ref`, allowing citizen listing and ownership checks without a reusable PI.
- **Token delivery**: the portal returns the token through the URL fragment. The service-provider frontend posts it to its backend, so the bearer token does not enter request URLs, referrers, or normal server access logs.
- **Key distribution**: S01 exposes its public ES256 key as JWKS. A configured private key is supported; the local in-memory demo may generate an ephemeral key because its consent records also disappear on restart.
- **Compatibility**: legacy `X-GBO-Consent-Id` and PI-plus-scope lookup paths are removed. Missing or legacy context fails closed.

---

## Phase 1: Signed consent happy path

**User stories**: As S01, I can issue cryptographic proof of exactly what was granted. As a service provider, I can present that proof with a DvTP query. As the PDP, I can authorize from verified context.

### What to build

Create a complete consent flow in which S01 signs the grant, the portal delivers the token, the service provider forwards it through FSC, and the PDP verifies and exposes its claims to DVT0001. Preserve the existing successful mortgage-data result.

### Acceptance criteria

- [ ] The consent response contains a signed token and no standalone PI.
- [ ] The token contains every claim defined in the architectural decisions.
- [ ] The DvTP frontend and backend use the token instead of resolving PI by consent ID.
- [ ] A valid token, matching FSC caller and matching GraphQL PI produces the existing ALLOW result.

---

## Phase 2: Fail-closed authorization binding

**User stories**: As a source holder, I can prove that subject, consumer, scope and consent all belong to the same authorization context. As a security reviewer, I can see deterministic denies for forged or mixed context.

### What to build

Enforce the signed bindings at the PDP and policy layers, including explicit caller-OIN comparison and removal of all legacy lookup fallback behavior.

### Acceptance criteria

- [ ] Changed PI, scopes, consumer OIN, audience, signature, or key ID is denied.
- [ ] Expired, not-yet-valid, malformed, missing, or legacy-only context is denied.
- [ ] The same PI and scope presented by a different FSC caller OIN is denied.
- [ ] Caller OIN is sourced only from validated FSC context.
- [ ] Deny reasons distinguish invalid context, actor mismatch, subject mismatch, scope mismatch, and status failure.

---

## Phase 3: Deterministic revocation

**User stories**: As a citizen, revoking one consent immediately stops requests backed by that consent. As a PDP, I never substitute a sibling consent for the referenced grant.

### What to build

Add the minimal S01 status representation and make the PDP check only the signed `consent_id`. Preserve policy-visible withdrawn and expired outcomes.

### Acceptance criteria

- [ ] An active signed consent authorizes when every other check passes.
- [ ] A revoked consent denies even while its token is otherwise valid.
- [ ] A missing consent status fails closed.
- [ ] Another active consent for the same citizen cannot rescue the request.
- [ ] Status responses contain no PI or signed authorization details.

---

## Phase 4: Remove PI from S01 persistence

**User stories**: As a citizen, I can still list and revoke my consents. As a privacy officer, I can verify that S01 does not persist or return PI.

### What to build

Replace the persisted PI with a consent-portal-specific `subject_ref`. Use it for citizen listing and ownership checks, while treating PI as ephemeral token-issuance input only.

### Acceptance criteria

- [ ] The in-memory and PostgreSQL stores contain no PI field or PI index.
- [ ] Create/list/get/status/revoke APIs do not return PI.
- [ ] Citizen list and revoke flows continue to enforce ownership using `subject_ref`.
- [ ] Existing legacy records cannot silently authorize new requests.
- [ ] Logs, traces, history payloads, and UI state do not expose PI outside the signed token path.

---

## Phase 5: Operations and regression evidence

**User stories**: As an operator, I can configure and rotate signing keys safely. As a maintainer, I can prove that security fixes did not break the DvTP scenarios.

### What to build

Complete key configuration, observability, documentation, and automated evidence across service, policy, frontend, and composed-flow boundaries.

### Acceptance criteria

- [ ] Startup rejects invalid key configuration and JWKS exposes only public material.
- [ ] `kid` supports an explicit current key and verification rejects unknown keys.
- [ ] Go tests, Rego tests, frontend type checks, and repository safety checks pass.
- [ ] Existing DvTP ALLOW/DENY outcomes and reason codes remain semantically equivalent where the authorization context is valid.
- [ ] The implementation documents the demo token transport and the remaining production consideration around short-lived proof-of-possession or refresh.
