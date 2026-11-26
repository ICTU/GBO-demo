// The way to MijnOverheid (the GBO toestemmingsportaal) and back. The
// citizen gives the Installatie Register consent to ask LVG whether the
// building is theirs. IR's backend pushes the request and keeps the consent
// token; the browser only follows the portal's address and brings back a
// one-time code (lib/consentFlow.ts).

import type { ConsentRequest } from './consentFlow'

declare global {
  interface Window {
    __GBO_CONFIG__?: {
      consentPortalUrl?: string
    }
  }
}

export const LVG_SCOPE = 'lvg:vbo:eigendom'

// envsubst leaves a variable it cannot fill as a literal ${VAR}: treat that as unset.
export function runtimeConfigValue(value?: string): string {
  const trimmed = value?.trim() ?? ''
  return /^\$\{.*\}$/.test(trimmed) ? '' : trimmed
}

type RuntimeConfig = Window['__GBO_CONFIG__']

// Configured explicitly. Only the link to "Mijn toestemmingen" needs it: the
// consent request gets the portal's address from the backend.
export function portalBase(
  config: RuntimeConfig = window.__GBO_CONFIG__,
  env: ImportMetaEnv = import.meta.env,
): string {
  return (
    runtimeConfigValue(config?.consentPortalUrl) ||
    env.VITE_CONSENT_PORTAL_URL ||
    'http://localhost:9002'
  ).replace(/\/$/, '')
}

// The consent IR asks for: the ownership check at LVG, for a year. The name
// and purpose the citizen sees come from IR's registration.
export function ownershipConsentRequest(now = new Date()): ConsentRequest {
  const validUntil = new Date(now)
  validUntil.setFullYear(now.getFullYear() + 1)
  return { service: 'installatieregister', scopes: [LVG_SCOPE], valid_until: validUntil.toISOString() }
}

export function myConsentsUrl(): string {
  return `${portalBase()}/mijnoverheid/toestemmingen`
}
