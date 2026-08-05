// Pass on the developer-portal's demo-session cookie so its watch-mode picks
// up the runs that developer started. An opaque id the dev-portal minted for
// itself — it says nothing about the citizen, and no cookie means no header.

const COOKIE = 'gbo_demo_session'

export function demoSessionHeader(): Record<string, string> {
  const m = new RegExp(`(?:^|;\\s*)${COOKIE}=([^;]*)`).exec(document.cookie)
  return m?.[1] ? { 'X-Demo-Session': decodeURIComponent(m[1]) } : {}
}
