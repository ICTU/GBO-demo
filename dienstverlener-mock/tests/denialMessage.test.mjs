import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { NAMED_DENIAL_CODES, denialMessage } from '../src/lib/denialMessage.ts'

const mentionsConsent = (m) => /toestemming/i.test(`${m.title} ${m.body}`)

test('a refused field offers to consent again, without naming a cause', () => {
  const m = denialMessage('FIELD_NOT_PERMITTED')
  assert.match(m.title, /niet ophalen/i)
  assert.match(m.body, /opnieuw toestemming/i)
  assert.doesNotMatch(`${m.title} ${m.body}`, /ingetrokken|verlopen/i)
  assert.equal(m.reconsent, true)
  assert.equal(m.retry, false)
})

test('the old consent codes are no longer named', () => {
  for (const code of ['CONSENT_WITHDRAWN', 'CONSENT_EXPIRED']) {
    assert.deepEqual(denialMessage(code), denialMessage(undefined))
  }
})

test('an administrative denial never mentions consent', () => {
  for (const code of ['UNAVAILABLE', 'ACCESS_DENIED', 'COVERAGE_UNVERIFIABLE', 'NO_DATA_FIELDS']) {
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
  assert.doesNotMatch(`${unknown.title} ${unknown.body}`, /SOME_FUTURE_CODE/)
})

test('a prototype key is not mistaken for a message', () => {
  for (const key of ['constructor', '__proto__', 'toString']) {
    assert.deepEqual(denialMessage(key), denialMessage(undefined))
  }
})

test('only codes the backend discloses are named here', async () => {
  const backend = await readFile(
    new URL('../../services/dienstverlener-backend/consumer/denial.go', import.meta.url),
    'utf8',
  )
  const block = backend.match(/disclosableDenyCodes = map\[string\]bool\{([^}]*)\}/)[1]
  const disclosable = [...block.matchAll(/"([A-Z_]+)"/g)].map((m) => m[1]).sort()
  assert.deepEqual(NAMED_DENIAL_CODES.slice().sort(), disclosable)
})

test('the citizen screen shows no policy reason code', async () => {
  const page = await readFile(new URL('../src/pages/Return.tsx', import.meta.url), 'utf8')
  assert.match(page, /trace-id/)
  assert.doesNotMatch(page, /reden <code>/)
})
