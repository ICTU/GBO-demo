export type RedirectContext = {
  service: string
  purpose: string
  scope: string[]
  client_oin: string
  client_name: string
  valid_until: string
  return_url: string
}

declare global {
  interface Window {
    __GBO_CONFIG__?: {
      consentPortalUrl?: string
    }
  }
}

export function resolvePortalBase(location: Pick<Location, 'hostname' | 'port' | 'protocol'>): string {
  const { hostname, port, protocol } = location

  // The local Docker Compose demo exposes both frontends on the same host,
  // on consecutive ports (default 9001/9002, overridable via GBO_PORT_*).
  // The portal lives one port up from the dienstverlener frontend.
  if (port !== '') {
    const current = parseInt(port, 10)
    if (!Number.isNaN(current)) {
      return `${protocol}//${hostname}:${current + 1}`
    }
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

const configuredPortalBase =
  typeof window === 'undefined'
    ? ''
    : window.__GBO_CONFIG__?.consentPortalUrl?.trim() ?? ''

const DEFAULT_PORTAL_BASE =
  configuredPortalBase ||
  (typeof window === 'undefined'
    ? 'http://localhost:9002'
    : resolvePortalBase(window.location))

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
