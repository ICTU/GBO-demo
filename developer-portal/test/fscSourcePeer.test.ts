import assert from 'node:assert/strict'
import { test } from 'node:test'
import { isFscSourcePeer } from '../src/util/fscSourcePeer.ts'

test('source-side FSC records include each consumer-authorized view', () => {
  for (const peer of ['bd', 'bd-via-hv', 'bd-via-edi', 'bd-via-asip']) {
    assert.equal(isFscSourcePeer(peer), true, peer)
  }
  for (const peer of ['hv', 'edi', 'asip', 'ir', 'bd-via-unknown', '']) {
    assert.equal(isFscSourcePeer(peer), false, peer)
  }
})
