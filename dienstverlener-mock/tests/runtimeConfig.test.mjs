import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import {
  DEFAULT_CONSUMER_PEER_ID,
  resolveConsumerPeerID,
} from '../src/lib/buildPortalUrl.ts'

test('uses the configured FSC consumer PeerID', () => {
  assert.equal(resolveConsumerPeerID(' 0000009950HYPBV00000 '), '0000009950HYPBV00000')
})

test('keeps the local demo PeerID as fallback', () => {
  assert.equal(resolveConsumerPeerID(''), DEFAULT_CONSUMER_PEER_ID)
})

test('renders the consumer PeerID from container runtime configuration', async () => {
  const template = await readFile(new URL('../runtime-config.js.template', import.meta.url), 'utf8')
  assert.match(template, /consumerPeerId: "\$\{CONSUMER_PEER_ID\}"/)
})
