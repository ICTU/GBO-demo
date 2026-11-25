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

test('#737: a backend without the test relying party hides its form', async () => {
  const { rpCatalogFrom } = await import('../src/lib/lifecycle.ts')
  assert.deepEqual(rpCatalogFrom(404, {}), { enabled: false })
  const body = { relying_parties: [{ id: 'a', name: 'GBO testpartij A', client_id: 'x509_hash:x' }], credentials: [] }
  assert.deepEqual(rpCatalogFrom(200, body), { enabled: true, error: '', parties: body.relying_parties, credentials: [] })
})

test('#737 D1: a request gives a link; a refusal says why', async () => {
  const { rpRequestFrom } = await import('../src/lib/lifecycle.ts')
  assert.deepEqual(rpRequestFrom(201, { link: 'openid4vp://?x', expires_at: at(5), session: 'r1' }), { link: 'openid4vp://?x', expires_at: at(5), session: 'r1' })
  assert.deepEqual(rpRequestFrom(400, { error: 'invalid_claims' }), { error: 'Kies een of meer claims, elk één keer.' })
})

test('#737 D2/D3: a presented copy is found in the issuance it came from', async () => {
  const { copyOrigin, describeRP, sessionChains } = await import('../src/lib/lifecycle.ts')
  const chains = sessionChains([
    event(0, 'session_started', 'a'),
    event(1, 'credential_issued', 'a', { configuration: 'test-pid-pid', key_fingerprints: ['fp1', 'fp2', 'fp3'] }),
  ])
  const presented = {
    at: at(5), kind: 'presented', session: 'r1', relying_party: 'b', relying_party_name: 'GBO testpartij B',
    configuration: 'test-pid-pid', vct: 'urn:eudi:pid:nl:1', claims: ['birthdate'], key_fingerprint: 'fp2', status: 'valid',
  }
  const origin = copyOrigin(chains, presented)
  assert.ok(origin && 'chain' in origin)
  assert.equal(origin.copy, 2)
  assert.equal(origin.of, 3)
  assert.equal(describeRP(presented, origin), 'getoond aan GBO testpartij B: test-pid-pid (birthdate) · exemplaar 2 van 3 · geldig')
  assert.equal(copyOrigin(chains, { ...presented, key_fingerprint: 'unknown' }), null)
  assert.equal(describeRP({ ...presented, status: 'revoked', key_fingerprint: 'x' }, null),
    'getoond aan GBO testpartij B: test-pid-pid (birthdate) · uitgifte niet in de tijdlijn · ingetrokken')
})

test('#737 D5/D8: declined and rejected presentations', async () => {
  const { describeRP } = await import('../src/lib/lifecycle.ts')
  const base = { at: at(1), session: 'r1', relying_party: 'a', relying_party_name: 'GBO testpartij A', configuration: 'test-pid-pid', vct: 'v', claims: ['birthdate'] }
  assert.equal(describeRP({ ...base, kind: 'declined', error: 'access_denied' }, null), 'geweigerd door de wallet voor GBO testpartij A: test-pid-pid (birthdate) (access_denied)')
  assert.equal(describeRP({ ...base, kind: 'rejected', error: 'invalid_presentation', error_description: 'oid4vp: nonce mismatch' }, null), 'afgewezen door GBO testpartij A: oid4vp: nonce mismatch')
})

test('#737: an answered request loses its link; so does an expired one', async () => {
  const { requestState } = await import('../src/lib/lifecycle.ts')
  const req = { session: 'r1', expires_at: at(5) }
  const presented = { at: at(2), kind: 'presented', session: 'r1', relying_party: 'a', relying_party_name: 'GBO testpartij A',
    configuration: 'test-pid-pid', vct: 'v', claims: ['birthdate'], key_fingerprint: 'fp', status: 'valid' }
  assert.deepEqual(requestState(req, [], new Date(at(1))), { state: 'open' })
  assert.deepEqual(requestState(req, [{ ...presented, session: 'other' }], new Date(at(1))), { state: 'open' })
  assert.deepEqual(requestState(req, [presented], new Date(at(3))), { state: 'answered', event: presented })
  assert.deepEqual(requestState(req, [], new Date(at(5))), { state: 'expired' })
})

test('a refused refresh says why, in the session it would continue', () => {
  assert.equal(
    describe(event(0, 'token_refused', 'a', { grant_type: 'refresh_token', error: 'invalid_grant', error_description: 'the deferral has expired' })),
    'refresh token · invalid_grant: the deferral has expired',
  )
})

test('#737 review: a copy is matched by fingerprint, type and time; two candidates are ambiguous', async () => {
  const { copyOrigin, describeRP, sessionChains } = await import('../src/lib/lifecycle.ts')
  const chains = sessionChains([
    event(0, 'credential_issued', 'old', { configuration: 'test-pid-pid', key_fingerprints: ['k1', 'k2'] }),
    event(10, 'credential_issued', 'new', { configuration: 'test-pid-pid', key_fingerprints: ['k3', 'k1'] }),
    event(12, 'credential_issued', 'other', { configuration: 'income', key_fingerprints: ['k1'] }),
  ])
  const presented = (minute: number, fp: string, configuration = 'test-pid-pid') => ({
    at: at(minute), kind: 'presented', session: 'r', relying_party: 'a', relying_party_name: 'GBO testpartij A',
    configuration, vct: 'v', claims: ['bsn'], key_fingerprint: fp, status: 'valid',
  })
  // Before the newer issuance only the older one can be the origin.
  const early = copyOrigin(chains, presented(5, 'k1'))
  assert.ok(early && 'chain' in early && early.chain.sessions[0] === 'old' && early.copy === 1)
  // After both, the same key is in two issuances of the type: ambiguous.
  assert.deepEqual(copyOrigin(chains, presented(15, 'k1')), { ambiguous: 2 })
  assert.equal(describeRP(presented(15, 'k1'), { ambiguous: 2 }),
    'getoond aan GBO testpartij A: test-pid-pid (bsn) · exemplaar in 2 uitgiftes, niet eenduidig · geldig')
  // Another type with the same key does not count.
  const income = copyOrigin(chains, presented(15, 'k1', 'income'))
  assert.ok(income && 'chain' in income && income.chain.sessions[0] === 'other')
  // A presentation before any issuance of it has no origin.
  assert.equal(copyOrigin(chains, presented(-1, 'k1')), null)
})
