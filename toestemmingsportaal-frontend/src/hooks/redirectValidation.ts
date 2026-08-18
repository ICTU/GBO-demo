export type RedirectContext = {
  service: string
  purpose: string
  scope: string[]
  client_oin: string
  client_name: string
  valid_until: string
  return_url: string
}

export function parseAllowedReturnOrigins(configured: string): Set<string> {
  const origins = new Set<string>()
  for (const value of configured.split(',')) {
    try {
      const url = new URL(value.trim())
      if ((url.protocol === 'http:' || url.protocol === 'https:') && !url.username && !url.password) {
        origins.add(url.origin)
      }
    } catch {
      // Invalid configuration entries are ignored; an empty set fails closed.
    }
  }
  return origins
}

function validatedReturnURL(value: unknown, allowedOrigins: Set<string>): string | null {
  if (typeof value !== 'string' || !value) return null
  try {
    const url = new URL(value)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null
    if (url.username || url.password) return null
    if (!allowedOrigins.has(url.origin)) return null
    return url.toString()
  } catch {
    return null
  }
}

export function validatedRedirectContext(
  value: unknown,
  allowedOrigins: Set<string>,
): RedirectContext | null {
  if (!value || typeof value !== 'object') return null
  const candidate = value as Partial<RedirectContext>
  const returnURL = validatedReturnURL(candidate.return_url, allowedOrigins)
  if (
    typeof candidate.service !== 'string' || !candidate.service
    || typeof candidate.purpose !== 'string' || !candidate.purpose
    || !Array.isArray(candidate.scope) || !candidate.scope.every((scope) => typeof scope === 'string')
    || typeof candidate.client_oin !== 'string' || !candidate.client_oin
    || typeof candidate.client_name !== 'string' || !candidate.client_name
    || typeof candidate.valid_until !== 'string'
    || !returnURL
  ) return null

  return {
    service: candidate.service,
    purpose: candidate.purpose,
    scope: candidate.scope.filter(Boolean),
    client_oin: candidate.client_oin,
    client_name: candidate.client_name,
    valid_until: candidate.valid_until,
    return_url: returnURL,
  }
}
