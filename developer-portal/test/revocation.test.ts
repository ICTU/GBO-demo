import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  attestationOfferFrom, confirmRevocation, entryFrom, revocationMessage, revocationOffersFrom,
  revokeEntryErrorFrom, revokeOutcomeFrom, revokedSummary, type RevocationOffer,
} from '../src/lib/revocation.ts'

const offer: RevocationOffer = {
  reference: 'devportal-1', kind: 'test_pid', label: 'Test-PID', bsn: '999991772', provider: 'test-pid', list: 'pid',
  offered_at: '2026-11-17T10:00:00Z', revoked_entries: 0,
}

test('B12/U1: a backend without the revocation routes hides the block', () => {
  assert.deepEqual(revocationOffersFrom(404, { error: 'not found' }), { enabled: false })
})

test('U1: the offers fill the overview; any other failure shows the block with a message', () => {
  assert.deepEqual(revocationOffersFrom(200, [offer]), { enabled: true, offers: [offer], error: '' })
  const failed = { enabled: true, offers: [], error: 'De aanbiedingen van het portal zijn niet beschikbaar.' }
  assert.deepEqual(revocationOffersFrom(502, {}), failed)
  assert.deepEqual(revocationOffersFrom(null, undefined), failed)
})

test('B2/U5: an attestation offer gives its link and reference', () => {
  assert.deepEqual(attestationOfferFrom(201, { link: 'openid-credential-offer://?x', offer: { reference: 'devportal-1' } }),
    { link: 'openid-credential-offer://?x', reference: 'devportal-1' })
  assert.deepEqual(attestationOfferFrom(201, { link: 'x' }), { error: revocationMessage('issuer_error') })
})

test('B3/B4: a refused offer shows the reason; no answer says the backend is unreachable', () => {
  assert.deepEqual(attestationOfferFrom(400, { error: 'unknown_offer' }), { error: revocationMessage('unknown_offer') })
  assert.deepEqual(attestationOfferFrom(404, { error: 'no_data' }), { error: revocationMessage('no_data') })
  assert.deepEqual(attestationOfferFrom(null, undefined), { error: 'De portal-backend is niet bereikbaar.' })
})

test('every reason has its own message; an unknown one names its code', () => {
  const codes = ['invalid_request', 'unknown_offer', 'unknown_reference', 'unknown_entry', 'offer_not_configured',
    'catalog_unavailable', 'no_data', 'source_unavailable', 'bad_source_data', 'issuer_unavailable', 'issuer_error']
  assert.equal(new Set(codes.map(revocationMessage)).size, codes.length)
  assert.equal(revocationMessage('something_new'), 'Mislukt (something_new).')
})

test('B5/U3: a revoked offer reports how many entries; one never taken reports that nothing more is issued', () => {
  const revoked = { ...offer, revoked_at: '2026-11-17T10:05:00Z', revoked_entries: 4 }
  assert.deepEqual(revokeOutcomeFrom(200, { offer: revoked }), { offer: revoked })
  assert.equal(revokedSummary(revoked), 'ingetrokken: 4 plekken')
  assert.equal(revokedSummary({ ...revoked, revoked_entries: 1 }), 'ingetrokken: 1 plek')
  assert.match(revokedSummary({ ...revoked, revoked_entries: 0 }), /nog niet opgehaald/)
  assert.equal(revokedSummary(offer), 'geldig')
  assert.deepEqual(revokeOutcomeFrom(404, { error: 'unknown_reference' }), { error: revocationMessage('unknown_reference') })
})

test('U2: the question before a revocation says it cannot be undone', () => {
  assert.match(confirmRevocation(offer), /definitief en niet terug te draaien/)
  assert.match(confirmRevocation(offer), /devportal-1/)
})

test('B7/U4: the entry form takes a provider, a list and an index of 0 or more', () => {
  assert.deepEqual(entryFrom(' test-pid ', 'pid', '7'), { provider: 'test-pid', list: 'pid', index: 7 })
  assert.deepEqual(entryFrom('test-pid', 'pid', '0'), { provider: 'test-pid', list: 'pid', index: 0 })
  for (const [provider, list, index] of [['', 'pid', '1'], ['test-pid', '', '1'], ['test-pid', 'pid', ''], ['test-pid', 'pid', '-1'], ['test-pid', 'pid', '1.5'], ['test-pid', 'pid', 'x']]) {
    assert.deepEqual(entryFrom(provider, list, index), { error: revocationMessage('invalid_request') })
  }
  assert.equal(revokeEntryErrorFrom(204, undefined), '')
  assert.equal(revokeEntryErrorFrom(404, { error: 'unknown_entry' }), revocationMessage('unknown_entry'))
  assert.equal(revokeEntryErrorFrom(502, { error: 'issuer_unavailable' }), revocationMessage('issuer_unavailable'))
})
