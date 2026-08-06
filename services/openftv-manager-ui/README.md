# OpenFTV management interface (demo wiring)

`realm-gbo.json` is a Keycloak realm imported at container start. Keycloak's
realm parser rejects unknown fields, so the explanation lives here instead of
in the JSON.

## Why Keycloak, and not something smaller

The UI reads its capabilities from a **top-level `roles` claim in the access
token** (`src/auth/capabilities.ts` upstream: `admin` may publish, `author`
may write, anything else is read-only). That is a Keycloak shape. The realm
ships an `oidc-usermodel-realm-role-mapper` that copies realm roles into that
claim; an IdP that only emits `groups` would leave the UI read-only.

## Users

| user | password | capability |
| --- | --- | --- |
| `admin` | `admin` | write + publish (deploy bundles) |
| `auteur` | `auteur` | write, no publish |
| `auditor` | `auditor` | read-only |

## Demo-only shortcuts

- `redirectUris` and `webOrigins` are `*`, because the UI's origin is
  deployment-specific (a LAN IP here, a hostname elsewhere) while this realm
  ships in git. Fine locally, not fine anywhere else.
- Keycloak runs `start-dev` with an in-memory database, so every restart
  re-imports the realm and discards any change made through its admin console.
- `sslRequired: none`, since the demo is served over plain HTTP on a LAN.

## Reaching it from another machine

`VITE_OIDC_AUTHORITY`, `VITE_PAP_BASE_URL` and `VITE_PIP_BASE_URL` are read by
the **browser**, not from inside the compose network. Set `GBO_PUBLIC_HOST` to
the host's LAN address before `docker compose up`, or the page will try to
reach `localhost` on the machine you are browsing from.
