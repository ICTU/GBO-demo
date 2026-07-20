import type { IssuancePayload, UsePayload, Tab } from '../types'

// Generates a curl-equivalent for the Submit action (bypasses the Vite
// proxy: uses direct host-ports so copy-paste into a terminal works).

export function curlForIssuance(p: IssuancePayload): string {
  const consentBody = {
    dienstverlener_oin: p.dienstverlener_oin,
    scopes: p.scopes,
    validity_seconds: p.validity_seconds ?? 7776000,
  }
  return [
    `# 1. login (BSN → JWT)`,
    `TOK=$(curl -sS -X POST http://localhost:9405/portal/login \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -d '${JSON.stringify({ citizen_bsn: p.citizen_bsn })}' | jq -r .token)`,
    ``,
    `# 2. consent aanmaken`,
    `curl -sS -X POST http://localhost:9405/portal/consents \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -H "Authorization: Bearer $TOK" \\`,
    `  -d '${JSON.stringify(consentBody)}'`,
  ].join('\n')
}

export function curlForUse(p: UsePayload): string {
  return [
    `curl -sS -X POST http://localhost:9406/api/dvtp/query \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -d '${JSON.stringify({
      consent_id: p.consent_id,
      scope_id: p.scope_id,
      belastingjaren: p.belastingjaren,
      fields: p.fields,
    })}'`,
  ].join('\n')
}

export function curlFor(tab: Tab, payload: IssuancePayload | UsePayload): string {
  return tab === 'issuance'
    ? curlForIssuance(payload as IssuancePayload)
    : curlForUse(payload as UsePayload)
}
