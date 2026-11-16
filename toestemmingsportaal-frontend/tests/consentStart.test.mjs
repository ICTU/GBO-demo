import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import {
  INITIAL_PHASE,
  START_COOKIE,
  START_ROUTE,
  clearedStartCookie,
  confirmation,
  nextPhase,
  readStartCookie,
  resumeAfterLogin,
  startCookie,
  startParams,
  startPath,
} from '../src/consentStart/startFlow.ts'
import { decideRequest, PortalApiError, resolveRequest } from '../src/api/portalClient.ts'

const src = new URL('../src/', import.meta.url)
const read = (path) => readFile(new URL(path, src), 'utf8')

const requestUri = 'urn:ietf:params:oauth:request_uri:abc-DEF_123'
const request = {
  client_id: '99999999900000000300',
  client_name: 'Demo Hypotheekverlener BV',
  service: 'hypotheek',
  purpose: 'Hypotheekaanvraag beoordelen',
  scopes: ['bd:ib:2025', 'bd:ib:2024'],
}

// The pages and modules of the start flow. The portal login token
// (usePortalToken) is the one thing kept in sessionStorage, a demo shortcut
// outside this flow.
const startFlowSources = [
  'consentStart/startFlow.ts',
  'pages/ToestemmingStart.tsx',
  'pages/ToestemmingConsent.tsx',
  'pages/ToestemmingBevestiging.tsx',
  'pages/ToestemmingFout.tsx',
  'api/portalClient.ts',
]

test('the start route reads client_id and request_uri and ignores any other parameter', () => {
  const start = startParams(
    `?client_id=99999999900000000300&request_uri=${encodeURIComponent(requestUri)}` +
      '&client_name=Evil&return_url=https%3A%2F%2Fevil.example&scope=bd%3Aib%3A2025&purpose=x',
  )
  assert.deepEqual(start, { clientId: '99999999900000000300', requestUri })
  assert.equal(
    startPath(start),
    `/toestemming/start?client_id=99999999900000000300&request_uri=${encodeURIComponent(requestUri)}`,
  )
})

test('a start URL without, with an empty or with a repeated client_id or request_uri is no start', () => {
  assert.equal(startParams(`?request_uri=${requestUri}`), null)
  assert.equal(startParams('?client_id=99999999900000000300'), null)
  assert.equal(startParams('?client_id=&request_uri=x'), null)
  assert.equal(startParams('?client_id=a&client_id=b&request_uri=x'), null)
  assert.equal(startParams('?client_id=a&request_uri=x&request_uri=y'), null)
})

test('the start URL waits in a five-minute SameSite=Lax cookie, Secure behind https', () => {
  const start = { clientId: '99999999900000000300', requestUri }
  const plain = startCookie(start, false)
  assert.match(plain, new RegExp(`^${START_COOKIE}=`))
  assert.match(plain, /; Max-Age=300; Path=\/; SameSite=Lax$/)
  assert.doesNotMatch(plain, /Secure/)
  assert.match(startCookie(start, true), /; SameSite=Lax; Secure$/)
  assert.match(clearedStartCookie(true), new RegExp(`^${START_COOKIE}=; Max-Age=0; Path=/; SameSite=Lax; Secure$`))

  const value = plain.split(';')[0]
  assert.equal(readStartCookie(`other=1; ${value}; gbo_demo_session=x`), startPath(start))
  assert.equal(readStartCookie('other=1'), null)
})

test('the cookie only leads back to the start route', () => {
  const cookie = (value) => `${START_COOKIE}=${encodeURIComponent(value)}`
  assert.equal(readStartCookie(cookie('https://evil.example/toestemming/start?client_id=a&request_uri=b')), null)
  assert.equal(readStartCookie(cookie('/mijnoverheid/toestemmingen')), null)
  assert.equal(readStartCookie(cookie('/toestemming/start?client_id=a')), null)
  assert.equal(
    readStartCookie(cookie('/toestemming/start?client_id=a&request_uri=b&return_url=https://evil.example')),
    '/toestemming/start?client_id=a&request_uri=b',
  )
  assert.equal(readStartCookie(`${START_COOKIE}=%E0%A4%A`), null)
})

test('the start flow keeps nothing in sessionStorage or localStorage', async () => {
  for (const path of startFlowSources) {
    const source = await read(path)
    assert.doesNotMatch(source, /sessionStorage|localStorage/, `${path} uses browser storage`)
  }
})

test('the consent screen appears only after a successful resolve', () => {
  assert.equal(INITIAL_PHASE.kind, 'resolving')
  assert.deepEqual(nextPhase(INITIAL_PHASE, { type: 'resolved', request }), { kind: 'consent', request })
  assert.deepEqual(nextPhase(INITIAL_PHASE, { type: 'failed' }), { kind: 'error' })
  // No decision before the request is resolved.
  assert.equal(nextPhase(INITIAL_PHASE, { type: 'deciding' }), INITIAL_PHASE)
  assert.equal(nextPhase(INITIAL_PHASE, { type: 'decided', answer: {} }), INITIAL_PHASE)
  // A resolve after an error does not bring the consent screen back.
  const error = { kind: 'error' }
  assert.equal(nextPhase(error, { type: 'resolved', request }), error)
})

test('a resolved request with no data sets, or one the portal cannot describe, is the error page', () => {
  assert.deepEqual(nextPhase(INITIAL_PHASE, { type: 'resolved', request: { ...request, scopes: [] } }), {
    kind: 'error',
  })
  assert.deepEqual(
    nextPhase(INITIAL_PHASE, { type: 'resolved', request: { ...request, scopes: ['bd:ib:2025', 'x:onbekend'] } }),
    { kind: 'error' },
  )
})

test('the consent screen offers only "Geef toestemming" and "Weiger"', async () => {
  const source = await read('pages/ToestemmingConsent.tsx')
  assert.match(source, />\s*Weiger\s*</)
  assert.match(source, /'Geef toestemming'/)
  assert.doesNotMatch(source, /Annuleren/)
  assert.equal([...source.matchAll(/<button\b/g)].length, 2)
  assert.doesNotMatch(source, /<a\b|href=|navigate|location/)
})

test('a decision shows the confirmation before the browser returns', async () => {
  const deciding = nextPhase({ kind: 'consent', request }, { type: 'deciding' })
  assert.deepEqual(deciding, { kind: 'deciding', request })
  const answer = { outcome: 'granted', client_name: 'Demo Hypotheekverlener BV', redirect_to: 'http://localhost:9001/return?code=c&state=s' }
  assert.deepEqual(nextPhase(deciding, { type: 'decided', answer }), {
    kind: 'confirmed',
    confirmation: { outcome: 'granted', clientName: 'Demo Hypotheekverlener BV', redirectTo: answer.redirect_to },
  })
  // Only the confirmation page leaves the portal, after AUTO_REDIRECT_MS.
  const confirmationPage = await read('pages/ToestemmingBevestiging.tsx')
  assert.match(confirmationPage, /setTimeout\(onReturn, AUTO_REDIRECT_MS\)/)
  const startPage = await read('pages/ToestemmingStart.tsx')
  assert.equal([...startPage.matchAll(/location\.assign\(/g)].length, 1)
  assert.match(startPage, /const redirectTo = phase\.kind === 'confirmed' \? phase\.confirmation\.redirectTo : null/)
})

test('the return URL is the backend answer, taken literally', () => {
  for (const redirect_to of [
    'http://localhost:9001/return?code=Zm9v%2Bbar&state=a%20b',
    'https://keyper.example/verzoek/terug?x=1&state=s&error=access_denied',
    'https://dv.example/return?state=s&error=server_error#keep',
  ]) {
    for (const outcome of ['granted', 'denied', 'failed']) {
      assert.equal(confirmation({ outcome, client_name: 'X', redirect_to })?.redirectTo, redirect_to)
    }
  }
})

test('a decision without a usable return URL is the error page', () => {
  const deciding = { kind: 'deciding', request }
  for (const answer of [
    null,
    { outcome: 'failed' },
    { outcome: 'failed', redirect_to: '' },
    { outcome: 'granted', redirect_to: '/relative' },
    { outcome: 'granted', redirect_to: 'javascript:alert(1)' },
    { outcome: 'other', redirect_to: 'https://dv.example/return' },
  ]) {
    assert.deepEqual(nextPhase(deciding, { type: 'decided', answer }), { kind: 'error' }, JSON.stringify(answer))
  }
  assert.deepEqual(nextPhase(deciding, { type: 'failed' }), { kind: 'error' })
})

test('the error page has no link, no button and no redirect', async () => {
  const source = await read('pages/ToestemmingFout.tsx')
  assert.doesNotMatch(source, /<a\b|href|<button|Link|navigate|location|setTimeout/)
  assert.match(source, /breadcrumb=\{\[\{ label: 'Home' \}\]\}/)
})

test('resolve and decision call the backend with the request_uri as one path segment', async (t) => {
  const calls = []
  const answers = [
    new Response(JSON.stringify(request), { status: 200 }),
    new Response(JSON.stringify({ outcome: 'denied', client_name: 'X', redirect_to: 'https://dv.example/r?error=access_denied&state=s' })),
    new Response(JSON.stringify({ error: 'unknown', reason: 'REQUEST_URI_UNKNOWN' }), { status: 404 }),
  ]
  t.mock.method(globalThis, 'fetch', async (url, init) => {
    calls.push({ url, init })
    return answers.shift()
  })
  globalThis.document = { cookie: '' }
  t.after(() => delete globalThis.document)

  assert.deepEqual(await resolveRequest('tok', requestUri, '99999999900000000300'), request)
  assert.equal(
    calls[0].url,
    '/portal-api/portal/requests/urn%3Aietf%3Aparams%3Aoauth%3Arequest_uri%3Aabc-DEF_123?client_id=99999999900000000300',
  )
  assert.equal(calls[0].init.headers.Authorization, 'Bearer tok')

  const answer = await decideRequest('tok', requestUri, { decision: 'deny' })
  assert.equal(answer.redirect_to, 'https://dv.example/r?error=access_denied&state=s')
  assert.equal(calls[1].url, '/portal-api/portal/requests/urn%3Aietf%3Aparams%3Aoauth%3Arequest_uri%3Aabc-DEF_123/decision')
  assert.equal(calls[1].init.method, 'POST')
  assert.deepEqual(JSON.parse(calls[1].init.body), { decision: 'deny' })

  await assert.rejects(resolveRequest('tok', requestUri, 'x'), (err) => {
    assert.ok(err instanceof PortalApiError)
    assert.equal(err.status, 404)
    assert.equal(err.reason, 'REQUEST_URI_UNKNOWN')
    return true
  })
})

test('after the login the cookie leads back to the start URL', () => {
  const start = { clientId: '99999999900000000300', requestUri }
  const cookie = startCookie(start, false).split(';')[0]
  assert.equal(resumeAfterLogin(`other=1; ${cookie}`), startPath(start))
})

test('an expired start cookie after the login leads to the error page, not the overview', () => {
  for (const cookieHeader of ['', 'other=1', `${START_COOKIE}=${encodeURIComponent('/mijnoverheid/toestemmingen')}`]) {
    const resume = resumeAfterLogin(cookieHeader)
    assert.equal(resume, START_ROUTE)
    // The start route without its parameters is the error page.
    assert.equal(startParams(new URL(resume, 'http://portal.invalid').searchParams), null)
  }
})

test('no other site may frame the portal, in development and in the built image', async () => {
  const { default: config, FRAME_HEADERS } = await import('../vite.config.ts')
  assert.deepEqual(FRAME_HEADERS, {
    'Content-Security-Policy': "frame-ancestors 'none'",
    'X-Frame-Options': 'DENY',
  })
  assert.equal(config.server.headers, FRAME_HEADERS)
  assert.equal(config.preview.headers, FRAME_HEADERS)

  const nginx = await readFile(new URL('../nginx.conf', import.meta.url), 'utf8')
  // At server level, and no location adds headers of its own (which would
  // drop the inherited ones).
  const server = nginx.slice(0, nginx.indexOf('location'))
  assert.match(server, /add_header Content-Security-Policy "frame-ancestors 'none'" always;/)
  assert.match(server, /add_header X-Frame-Options DENY always;/)
  assert.equal([...nginx.matchAll(/add_header/g)].length, 2)
})
