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
| `fds` | `FTV_MANAGER_AUDITOR_PASSWORD` (`FDSSecret` in the simulation) | read-only |
| `deployment` | `FTV_MANAGER_DEPLOY_PASSWORD` | internal seed process; write + publish |

There is no public admin or author account. Public demo visitors may inspect
policies, deployments and the Logboek as `auditor`; the synthetic simulation
must not contain personal data, tokens or secrets. The Manager API enforces
the same permissions with Cedar, so bypassing the UI does not bypass the
read-only restriction.

Set `KEYCLOAK_ADMIN_PASSWORD`, `FTV_MANAGER_AUDITOR_PASSWORD` and
`FTV_MANAGER_DEPLOY_PASSWORD` before running `make demo-manager`. Store them
as CI/CD secrets; none belongs in frontend configuration. The deployment
identity uses a short-lived OIDC access token and is only intended for
`scripts/seed-openftv-manager.py`.

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
