import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  describe, lifecycleEventsFrom, lifecycleMessage, lifecycleSettingsFrom, sessionChains, wholeNumber,
  type LifecycleEvent,
} from '../src/lib/lifecycle.ts'

const at = (minute: number) => `2026-11-25T12:${String(minute).padStart(2, '0')}:00Z`

const event = (minute: number, kind: string, session: string, extra: Partial<LifecycleEvent> = {}): LifecycleEvent => ({
  at: at(minute), kind, provider: 'test-pid', profile: 'eudi', session, ...extra,
})

test('a backend without the lifecycle routes hides the block', () => {
  assert.deepEqual(lifecycleSettingsFrom(404, {}), { enabled: false })
})

test('the settings fill the block; a failure shows the block with the reason', () => {
  const body = { providers: [{ name: 'test-pid', batch_size: 4, overridden: false }], configurations: [] }
  assert.deepEqual(lifecycleSettingsFrom(200, body), { enabled: true, error: '', ...body })
  const failed = lifecycleSettingsFrom(502, { error: 'issuer_unavailable' })
  assert.equal(failed.enabled && failed.error, lifecycleMessage(502, { error: 'issuer_unavailable' }))
})

test('A4: a refused setting shows the issuer\'s reason', () => {
  assert.equal(
    lifecycleMessage(400, { error: 'invalid_setting', error_description: 'the batch size of a reuse policy must be 2 or more' }),
    'De issuer weigert deze instelling: the batch size of a reuse policy must be 2 or more',
  )
  assert.equal(lifecycleMessage(null, undefined), 'De portal-backend is niet bereikbaar.')
})

test('the events answer', () => {
  assert.deepEqual(lifecycleEventsFrom(200, { events: [] }), [])
  assert.ok('error' in (lifecycleEventsFrom(503, { error: 'events_unavailable' }) as object))
})

test('form fields take whole numbers from a minimum', () => {
  assert.equal(wholeNumber('3', 2), 3)
  assert.equal(wholeNumber('1', 2), null)
  assert.equal(wholeNumber('2.5', 1), null)
  assert.equal(wholeNumber('', 0), null)
  assert.equal(wholeNumber('0', 0), 0)
})

test('C3: a refresh joins the chain of the session it continues; latest chain first', () => {
  const events = [
    event(0, 'session_started', 'a'),
    event(1, 'session_started', 'b'),
    event(2, 'token_issued', 'a'),
    event(3, 'credential_issued', 'a'),
    event(10, 'token_issued', 'a2', { previous_session: 'a', grant_type: 'refresh_token' }),
    event(11, 'credential_issued', 'a2'),
    event(20, 'token_issued', 'a3', { previous_session: 'a2', grant_type: 'refresh_token' }),
  ]
  const chains = sessionChains(events)
  assert.equal(chains.length, 2)
  assert.deepEqual(chains[0].sessions, ['a', 'a2', 'a3'])
  assert.equal(chains[0].events.length, 6)
  assert.deepEqual(chains[1].sessions, ['b'])
})

test('C1: an issuance says how many copies, keys and which batch size, and where it comes from', () => {
  assert.equal(
    describe(event(0, 'credential_issued', 'a', {
      configuration: 'test-pid-pid', credentials: 3, keys_offered: 3, batch_size: 3, batch_size_from: 'reuse_policy',
      proof_type: 'attestation',
    })),
    '3 exemplaren van test-pid-pid · 3 sleutels aangeboden · batch size 3 (beleid van het type) · attestation proof (key attestation)',
  )
  assert.equal(
    describe(event(0, 'credential_refused', 'a', {
      error: 'invalid_proof', error_description: 'at most 3 keys per request', keys_offered: 4, batch_size: 3,
      batch_size_from: 'reuse_policy',
    })),
    'invalid_proof: at most 3 keys per request · 4 sleutels aangeboden · batch size 3 (beleid van het type)',
  )
  assert.equal(
    describe(event(0, 'token_issued', 'a', { grant_type: 'refresh_token', wallet_provider: 'eudi-reference' })),
    'refresh token · wallet provider eudi-reference',
  )
})

test('a refused refresh says why, in the session it would continue', () => {
  assert.equal(
    describe(event(0, 'token_refused', 'a', { grant_type: 'refresh_token', error: 'invalid_grant', error_description: 'the deferral has expired' })),
    'refresh token · invalid_grant: the deferral has expired',
  )
})
