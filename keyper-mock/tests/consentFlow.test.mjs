import assert from 'node:assert/strict'
import { readFile, readdir } from 'node:fs/promises'
import test from 'node:test'
import { ownershipConsentRequest } from '../src/lib/portal.ts'
import {
  completeConsent,
  ConsentFlowError,
  portalAddress,
  readPortalReturn,
  startConsent,
} from '../src/lib/consentFlow.ts'

const portal = 'https://toestemmingsportaal.example/toestemming/start?client_id=99999999900000001000&request_uri=urn%3Aietf%3Aparams%3Aoauth%3Arequest_uri%3Aabc'

function fakeFetch(answers) {
  const calls = []
  const fetchImpl = async (path, init) => {
    calls.push({ path, init })
    const { status = 200, body = {} } = answers.shift()
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
  }
  return { calls, fetchImpl }
}

test('start asks the backend to push the request and answers with the portal address', async () => {
  const { calls, fetchImpl } = fakeFetch([{ body: { redirect_to: portal } }])
  const request = ownershipConsentRequest(new Date('2026-11-10T09:00:00Z'))
  assert.equal(await startConsent(request, fetchImpl), portal)
  assert.equal(calls.length, 1)
  assert.equal(calls[0].path, '/api/consent/start')
  assert.equal(calls[0].init.method, 'POST')
  assert.equal(calls[0].init.headers['Content-Type'], 'application/json')
  assert.deepEqual(JSON.parse(calls[0].init.body), request)
})

test('a refused start or a portal address that is not http(s) is an error, not a redirect', async () => {
  const refused = fakeFetch([{ status: 400, body: { error: 'invalid_scope', reason: 'UNKNOWN_SCOPE' } }])
  await assert.rejects(startConsent({ service: 's', scopes: ['x'] }, refused.fetchImpl), (err) => {
    assert.ok(err instanceof ConsentFlowError)
    assert.equal(err.status, 400)
    assert.equal(err.code, 'invalid_scope')
    return true
  })
  for (const redirect_to of ['javascript:alert(1)', '/toestemming/start', '', undefined]) {
    const broken = fakeFetch([{ body: { redirect_to } }])
    await assert.rejects(startConsent({ service: 's', scopes: ['x'] }, broken.fetchImpl), ConsentFlowError)
  }
  assert.equal(portalAddress('http://localhost:9002/toestemming/start'), 'http://localhost:9002/toestemming/start')
})

test('the return carries a code or an error, with the state; nothing else counts', () => {
  assert.deepEqual(readPortalReturn('?code=c-1&state=s-1'), { code: 'c-1', state: 's-1' })
  assert.deepEqual(readPortalReturn('?error=access_denied&state=s-1'), { error: 'access_denied', state: 's-1' })
  assert.equal(readPortalReturn('?code=c-1'), null)
  assert.equal(readPortalReturn('?code=c-1&error=access_denied&state=s-1'), null)
  assert.equal(readPortalReturn('?status=ok&consent_id=c-1'), null)
  assert.equal(readPortalReturn(''), null)
})

test('complete hands the code and state to the backend, which keeps the token', async () => {
  const { calls, fetchImpl } = fakeFetch([{ body: { outcome: 'granted', consent_id: 'c-1', scopes: ['bd:ib:2025'] } }])
  assert.deepEqual(await completeConsent({ code: 'code-1', state: 's-1' }, fetchImpl), {
    outcome: 'granted',
    consent_id: 'c-1',
    scopes: ['bd:ib:2025'],
  })
  assert.equal(calls[0].path, '/api/consent/complete')
  assert.deepEqual(JSON.parse(calls[0].init.body), { code: 'code-1', state: 's-1' })

  const denied = fakeFetch([{ body: { outcome: 'denied' } }])
  assert.equal((await completeConsent({ error: 'access_denied', state: 's-1' }, denied.fetchImpl)).outcome, 'denied')
  const odd = fakeFetch([{ body: { outcome: 'something' } }])
  assert.equal((await completeConsent({ error: 'server_error', state: 's-1' }, odd.fetchImpl)).outcome, 'failed')
  const mismatch = fakeFetch([{ status: 400, body: { error: 'state_mismatch' } }])
  await assert.rejects(completeConsent({ code: 'c', state: 'other' }, mismatch.fetchImpl), ConsentFlowError)
})

test('IR asks consent for the LVG ownership check, for a year', () => {
  assert.deepEqual(ownershipConsentRequest(new Date('2026-11-10T09:00:00Z')), {
    service: 'installatieregister',
    scopes: ['lvg:vbo:eigendom'],
    valid_until: '2027-11-10T09:00:00.000Z',
  })
})

test('the browser keeps no consent token or code, and the ownership check sends no token', async () => {
  const src = new URL('../src/', import.meta.url)
  const files = await readdir(src, { recursive: true })
  for (const file of files.filter((f) => /\.tsx?$/.test(f))) {
    const source = await readFile(new URL(file, src), 'utf8')
    assert.doesNotMatch(source, /consent_token|consentToken/, `${file} handles a consent token`)
  }
  // IR's own mock state lives in sessionStorage (lib/aanvraag.ts); it holds
  // the consent's id, never the token or the code.
  const aanvraag = await readFile(new URL('lib/aanvraag.ts', src), 'utf8')
  assert.match(aanvraag, /consentId\?: string/)
  assert.doesNotMatch(aanvraag, /\bcode\b/)
  for (const file of files.filter((f) => /\.tsx?$/.test(f) && f !== 'lib/aanvraag.ts')) {
    const source = await readFile(new URL(file, src), 'utf8')
    assert.doesNotMatch(source, /sessionStorage|localStorage/, `${file} uses browser storage`)
  }
})

test('the return page takes the code out of the address bar and completes once', async () => {
  const source = await readFile(new URL('../src/screens/eigenaar.tsx', import.meta.url), 'utf8')
  assert.match(source, /history\.replaceState\(window\.history\.state, '', window\.location\.pathname\)/)
  assert.equal([...source.matchAll(/completeConsent\(/g)].length, 1)
  assert.match(source, /started\.current = true/)
})
