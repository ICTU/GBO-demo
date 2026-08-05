// Pass on the developer-portal's demo-session cookie so its watch-mode picks
// up the runs that developer started. Nothing here depends on it: no cookie
// means no header, and a citizen using this mock has no dev-portal at all.

const COOKIE = 'gbo_demo_session'

export function demoSessionHeader(): Record<string, string> {
  const m = new RegExp(`(?:^|;\\s*)${COOKIE}=([^;]*)`).exec(document.cookie)
  return m?.[1] ? { 'X-Demo-Session': decodeURIComponent(m[1]) } : {}
}
