import { useEffect, useState } from 'react'

const STORAGE_KEY = 'gbo.portal_token'

function decodeJwtPayload(token: string): { sub?: string; exp?: number } | null {
  try {
    const [, payload] = token.split('.')
    return JSON.parse(atob(payload))
  } catch {
    return null
  }
}

function read(): { token: string; bsn?: string } | null {
  const token = sessionStorage.getItem(STORAGE_KEY)
  if (!token) return null
  const claims = decodeJwtPayload(token)
  if (!claims) return null
  if (claims.exp && claims.exp * 1000 < Date.now()) {
    sessionStorage.removeItem(STORAGE_KEY)
    return null
  }
  return { token, bsn: claims.sub }
}

export function usePortalToken() {
  const [state, setState] = useState(() => read())

  useEffect(() => {
    const onStorage = () => setState(read())
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [])

  const set = (token: string) => {
    sessionStorage.setItem(STORAGE_KEY, token)
    setState(read())
  }
  const clear = () => {
    sessionStorage.removeItem(STORAGE_KEY)
    setState(null)
  }

  return { token: state?.token ?? null, bsn: state?.bsn, set, clear }
}

export function getStoredToken(): string | null {
  return read()?.token ?? null
}
