export type RedirectContext = {
  service: string
  purpose: string
  scope: string[]
  client_oin: string
  client_name: string
  valid_until: string
  return_url: string
}

export function resolvePortalBase(location: Pick<Location, 'hostname' | 'port' | 'protocol'>): string {
  const { hostname, port, protocol } = location

  // The local Docker Compose demo exposes both frontends on the same host,
  // using ports 9001 and 9002.
  if (hostname === 'localhost' || hostname === '127.0.0.1' || port === '9001') {
    return `${protocol}//${hostname}:9002`
  }

  // Deployed environments expose each frontend on its own sibling hostname,
  // without the local development port.
  const labels = hostname.split('.')
  if (labels.length > 1) {
    labels[0] = 'toestemmingsportaal'
    return `${protocol}//${labels.join('.')}`
  }

  return `${protocol}//${hostname}:9002`
}

const DEFAULT_PORTAL_BASE =
  typeof window === 'undefined'
    ? 'http://localhost:9002'
    : resolvePortalBase(window.location)

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
