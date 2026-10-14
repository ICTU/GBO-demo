import { test } from 'node:test'
import assert from 'node:assert/strict'
import { display, expectedOf, requestBody, summary, traceOf, type AsiRequestView } from '../src/lib/asiRequest.ts'

const familyName = 'https://gbo.example/attributes/family_name/1'
const nationality = 'https://gbo.example/attributes/nationality/1'

test('P2: a verify sends each value as JSON', () => {
  assert.deepEqual(
    requestBody({ operation: 'verify', attributes: [{ identifier: familyName, value: '{"family_name":"Jansen"}' }] }),
    { body: { operation: 'verify', attributes: [{ identifier: familyName, value: { family_name: 'Jansen' } }] } },
  )
})

test('P2: a retrieve sends no values', () => {
  assert.deepEqual(
    requestBody({ operation: 'retrieve', attributes: [{ identifier: nationality, value: 'not even JSON' }] }),
    { body: { operation: 'retrieve', attributes: [{ identifier: nationality }] } },
  )
})

test('P2: a value that is not JSON is refused in the portal, by label', () => {
  assert.deepEqual(
    requestBody({ operation: 'verify', attributes: [{ identifier: familyName, value: '{family_name:' }] }, { [familyName]: 'Familienaam' }),
    { error: 'De waarde van Familienaam is geen JSON.' },
  )
  assert.deepEqual(requestBody({ operation: 'verify', attributes: [] }), { error: 'Kies ten minste één attribuut.' })
})

const done = (results: AsiRequestView['results'], operation: 'verify' | 'retrieve' = 'verify'): AsiRequestView => ({
  id: 'r', start_url: '', status: 'done', operation, seal: { valid: true, signer: 'CN=seal' }, results,
})

test('P3: the outcome per attribute, and what a saved scenario expects', () => {
  for (const [result, expected] of [['Match', 'match'], ['MatchWithVariation', 'match-with-variation'], ['NoMatch', 'no-match'], ['Unknown', 'unknown']] as const) {
    const v = done([{ identifier: familyName, label: 'Familienaam', result }])
    assert.equal(expectedOf(v), expected)
    assert.equal(display(v.results![0]), result)
  }
  const retrieved = done([{ identifier: nationality, label: 'Nationaliteit', value: { nationalities: ['NL', 'BE'] } }], 'retrieve')
  assert.equal(expectedOf(retrieved), 'retrieve')
  assert.equal(display(retrieved.results![0]), '{"nationalities":["NL","BE"]}')
  assert.equal(summary(retrieved), 'Zegel geldig, verzegeld door CN=seal.')
})

test('P3: a denial, a failure and an invalid seal give no expected outcome', () => {
  const denied: AsiRequestView = { id: 'r', start_url: '', status: 'denied', operation: 'verify', error: 'access_denied' }
  assert.equal(summary(denied), 'Geweigerd (access_denied). Er is niets bij de bron opgevraagd.')
  assert.equal(expectedOf(denied), undefined)
  const failed: AsiRequestView = { id: 'r', start_url: '', status: 'failed', operation: 'verify', asip_status: 401, problem: 'Unauthorized: invalid token' }
  assert.equal(summary(failed), 'De ASIP gaf status 401: Unauthorized: invalid token')
  const unsealed: AsiRequestView = { ...done([]), seal: { valid: false, error: 'unknown anchor' } }
  assert.equal(summary(unsealed), 'Zegel ongeldig — resultaat niet geaccepteerd. unknown anchor')
  assert.equal(expectedOf(unsealed), undefined)
})

test('the chain of a request is the trace the QTSP reports for it, once it has ended', () => {
  const v: AsiRequestView = { ...done([{ identifier: familyName, label: 'Familienaam', result: 'Match' }]), trace_id: 'a'.repeat(32) }
  assert.equal(traceOf(v), 'a'.repeat(32))
  assert.equal(traceOf({ ...v, status: 'pending' }), null)
  assert.equal(traceOf({ id: 'r', start_url: '', status: 'denied', operation: 'verify', error: 'access_denied' }), null)
})
