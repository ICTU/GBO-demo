export type RedirectContext = {
  service: string
  purpose: string
  scope: string[]
  client_oin: string
  client_name: string
  valid_until: string
  return_url: string
}

const DEFAULT_PORTAL_BASE =
  typeof window !== 'undefined' && window.location.hostname === 'localhost'
    ? 'http://localhost:9002'
    : `${window.location.protocol}//${window.location.hostname.replace('9001', '9002')}:9002`

export function buildPortalUrl(ctx: RedirectContext, portalBase: string = DEFAULT_PORTAL_BASE): string {
  const params = new URLSearchParams({
    service: ctx.service,
    purpose: ctx.purpose,
    scope: ctx.scope.join(','),
    client_oin: ctx.client_oin,
    client_name: ctx.client_name,
    valid_until: ctx.valid_until,
    return_url: ctx.return_url,
  })
  return `${portalBase}/auth?${params.toString()}`
}
