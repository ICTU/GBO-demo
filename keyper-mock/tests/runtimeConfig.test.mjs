import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import {
  consumerPeerId,
  DEFAULT_IR_PEER_ID,
  portalBase,
  runtimeConfigValue,
} from '../src/lib/portal.ts'

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
  const variables = [...template.matchAll(/\$\{?(\w+)\}?/g)].map((m) => m[1])
  assert.ok(variables.length > 0)
  for (const name of variables) {
    assert.match(dockerfile, new RegExp(`\\b${name}=""`), `${name} is not declared`)
  }
})

test('unfilled runtime-config variables fall back to the defaults', () => {
  const unfilled = { consentPortalUrl: '${CONSENT_PORTAL_URL}', consumerPeerId: '${CONSUMER_PEER_ID}' }
  assert.equal(portalBase(unfilled, {}), 'http://localhost:9002')
  assert.equal(consumerPeerId(unfilled, {}), DEFAULT_IR_PEER_ID)
  const configured = { consentPortalUrl: 'https://portal.example/', consumerPeerId: '12345678901234567890' }
  assert.equal(portalBase(configured, {}), 'https://portal.example')
  assert.equal(consumerPeerId(configured, {}), '12345678901234567890')
})
