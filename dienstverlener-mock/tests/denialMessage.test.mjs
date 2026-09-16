import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { NAMED_DENIAL_CODES, denialMessage } from '../src/lib/denialMessage.ts'

const mentionsConsent = (m) => /toestemming/i.test(`${m.title} ${m.body}`)

test('a revoked consent is named, and says so without hedging', () => {
  const m = denialMessage('CONSENT_WITHDRAWN')
  assert.match(m.title, /ingetrokken/i)
  assert.doesNotMatch(m.body, /kan komen doordat|mogelijk|misschien/i)
  assert.equal(m.reconsent, true)
  // Retrying cannot help: the consent is gone until the citizen grants it again.
  assert.equal(m.retry, false)
})

test('an expired consent gets its own message, distinct from a revoked one', () => {
  const expired = denialMessage('CONSENT_EXPIRED')
  const revoked = denialMessage('CONSENT_WITHDRAWN')
  assert.match(expired.title, /verlopen/i)
  assert.notEqual(expired.title, revoked.title)
  assert.notEqual(expired.body, revoked.body)
  assert.equal(expired.retry, false)
})

test('an administrative denial never mentions consent', () => {
  // The backend collapses these to UNAVAILABLE, but assert on the UI's own
  // behaviour: anything it is not willing to name must stay silent about
  // consent, whatever code arrives.
  for (const code of ['UNAVAILABLE', 'ACTOR_NOT_ALLOWED', 'CONSTRAINT_MISMATCH', 'NO_APPLICABLE_RULE']) {
    const m = denialMessage(code)
    assert.equal(mentionsConsent(m), false, `${code} mentions consent`)
  }
})

test('a transport failure never mentions consent', () => {
  for (const absent of [undefined, null, '']) {
    assert.equal(mentionsConsent(denialMessage(absent)), false)
  }
})

test('an unknown code falls through instead of being rendered', () => {
  const generic = denialMessage(undefined)
  const unknown = denialMessage('SOME_FUTURE_CODE')
  assert.deepEqual(unknown, generic)
  // The raw code must not leak into the sentence.
  assert.doesNotMatch(`${unknown.title} ${unknown.body}`, /SOME_FUTURE_CODE/)
})

test('a prototype key is not mistaken for a message', () => {
  // denial_code arrives over the network; an object lookup would answer here.
  for (const key of ['constructor', '__proto__', 'toString']) {
    assert.deepEqual(denialMessage(key), denialMessage(undefined))
  }
})

test('only codes the backend discloses are named here', async () => {
  const backend = await readFile(
    new URL('../../services/dienstverlener-backend/denial.go', import.meta.url),
    'utf8',
  )
  const block = backend.match(/disclosableDenyCodes = map\[string\]bool\{([^}]*)\}/)[1]
  const disclosable = [...block.matchAll(/"([A-Z_]+)"/g)].map((m) => m[1]).sort()
  assert.deepEqual(NAMED_DENIAL_CODES.slice().sort(), disclosable)
})

test('the citizen screen shows no policy reason code', async () => {
  const page = await readFile(new URL('../src/pages/Return.tsx', import.meta.url), 'utf8')
  // #121: the trace-id stays as something to quote to a helpdesk; the reason
  // does not. Guards against it being reinstated by accident.
  assert.match(page, /trace-id/)
  assert.doesNotMatch(page, /reden <code>/)
})
