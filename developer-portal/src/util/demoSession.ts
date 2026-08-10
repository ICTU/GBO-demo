// Which developer's browser a flow came from, so watch-mode picks up your own
// runs. A cookie rather than a URL-parameter because cookie scope ignores the
// port, so one reaches all three frontends. Forgeable — never gate data on it.

const COOKIE = 'gbo_demo_session'

function read(): string {
  const m = new RegExp(`(?:^|;\\s*)${COOKIE}=([^;]*)`).exec(document.cookie)
  return m ? decodeURIComponent(m[1]) : ''
}

// cookieDomain returns the widest domain still covering only this demo.
// Deployed the frontends are siblings, so the parent covers them; on a bare
// host like `localhost` a host-only cookie already spans every port.
function cookieDomain(hostname: string): string {
  const labels = hostname.split('.')
  return labels.length > 2 ? labels.slice(1).join('.') : ''
}

function write(id: string) {
  const attrs = `path=/; max-age=86400; SameSite=Lax`
  const domain = cookieDomain(window.location.hostname)
  if (domain) {
    document.cookie = `${COOKIE}=${id}; domain=${domain}; ${attrs}`
    // A refused domain (a public suffix like `co.uk`) is dropped silently —
    // fall back to host-only so the dev-portal at least knows its own runs.
    if (read() === id) return
  }
  document.cookie = `${COOKIE}=${id}; ${attrs}`
}

// newId mints a session id, without requiring a secure context.
//
// crypto.randomUUID is only exposed on https: and localhost, so on a demo
// served over plain http from a LAN address it is undefined — and calling it
// threw before any request was made, which broke every button in the portal
// with "crypto.randomUUID is not a function". That is precisely the setup a
// demo runs in, so this has to work there.
//
// Math.random is fine here: the id only says "same browser" so watch-mode can
// filter to your own runs. It is a correlation tag, not a credential — the
// header it ends up in is forgeable by design (see the note at the top), so
// nothing is gated on it being unguessable.
function newId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  return 'demo-' + Math.random().toString(36).slice(2) + Date.now().toString(36)
}

// demoSessionId returns this browser's id, minting one on first use. Shared
// by every dev-portal tab — that is one developer, and a run they start
// belongs to all their tabs equally.
export function demoSessionId(): string {
  const existing = read()
  if (existing) return existing
  const id = newId()
  write(id)
  // With cookies off this stays empty, and every watcher falls back to the
  // old see-everything behaviour rather than breaking.
  return read()
}

// demoSessionHeader tags a request the dev-portal makes itself, so other
// sessions skip it. Empty leaves the request untagged and visible to all.
export function demoSessionHeader(): Record<string, string> {
  const id = demoSessionId()
  return id ? { 'X-Demo-Session': id } : {}
}
