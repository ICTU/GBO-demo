import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import {
  DEFAULT_CONSUMER_PEER_ID,
  resolveConsumerPeerID,
  runtimeConfigValue,
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

test('treats a variable envsubst left unfilled as unset', () => {
  assert.equal(runtimeConfigValue('${CONSENT_PORTAL_URL}'), '')
  assert.equal(runtimeConfigValue(' https://portal.example '), 'https://portal.example')
  assert.equal(runtimeConfigValue(undefined), '')
})

test('the image declares every runtime-config variable', async () => {
  const root = new URL('../', import.meta.url)
  const [dockerfile, template] = await Promise.all([
    readFile(new URL('Dockerfile', root), 'utf8'),
    readFile(new URL('runtime-config.js.template', root), 'utf8'),
  ])
  const variables = [...template.matchAll(/\$\{(\w+)\}/g)].map((m) => m[1])
  assert.ok(variables.length > 0)
  for (const name of variables) {
    assert.match(dockerfile, new RegExp(`\\b${name}=""`), `${name} is not declared`)
  }
})
