import { test } from 'node:test'
import assert from 'node:assert/strict'
import { personLabel, testPersonsFrom, testPidMessage, testPidOfferFrom } from '../src/lib/testPid.ts'

test('F1: every reason has its own message', () => {
  const codes = [
    'unknown_person', 'no_data', 'test_pid_not_configured', 'source_unavailable',
    'bad_source_data', 'issuer_unavailable', 'issuer_error',
  ]
  const messages = codes.map(testPidMessage)
  assert.equal(new Set(messages).size, codes.length)
  for (const message of messages) assert.doesNotMatch(message, /Geen test-PID-offer \(/)
})

test('F1: an unknown reason names its code', () => {
  assert.equal(testPidMessage('something_new'), 'Geen test-PID-offer (something_new).')
})

test('F2: a backend without the test PID routes hides the block', () => {
  assert.deepEqual(testPersonsFrom(404, { error: 'not found' }), { enabled: false })
})

test('F2: the persons fill the list', () => {
  const persons = [{ bsn: '999991772', name: 'Frouke Jansen' }]
  assert.deepEqual(testPersonsFrom(200, persons), { enabled: true, persons, error: '' })
})

test('F2: any other failure shows the block with a message', () => {
  const failed = { enabled: true, persons: [], error: 'Testpersonen niet beschikbaar.' }
  assert.deepEqual(testPersonsFrom(502, {}), failed)
  assert.deepEqual(testPersonsFrom(null, undefined), failed)
  assert.deepEqual(testPersonsFrom(200, { persons: [] }), failed)
})

test('F3: a person shows by name and BSN', () => {
  assert.equal(personLabel({ bsn: '999991772', name: 'Frouke Jansen' }), 'Frouke Jansen (999991772)')
})

test('F4: an offer gives its link', () => {
  assert.deepEqual(testPidOfferFrom(201, { link: 'openid-credential-offer://?x' }), { link: 'openid-credential-offer://?x' })
})

test('F4: an offer without a link is an issuer error, not an empty QR code', () => {
  assert.deepEqual(testPidOfferFrom(201, {}), { error: testPidMessage('issuer_error') })
})

test('F4: a refusal shows the message of its reason', () => {
  assert.deepEqual(testPidOfferFrom(404, { error: 'no_data' }), { error: testPidMessage('no_data') })
  assert.deepEqual(testPidOfferFrom(500, 'Internal Server Error'), { error: 'Geen test-PID-offer (HTTP 500).' })
})
