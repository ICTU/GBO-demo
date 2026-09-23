import assert from 'node:assert/strict'
import test from 'node:test'
import { outcomeOf } from '../src/lib/ownership.ts'
import { readPortalReturn } from '../src/lib/portal.ts'

const VBO = '0632010000099412'

test('LVG answering with the requested VBO-id confirms ownership', () => {
  assert.equal(outcomeOf({ allowed: true, data: { data: { vbo: { vboId: VBO } } } }, VBO), 'owner')
})

test('LVG answering null means the citizen does not own it', () => {
  assert.equal(outcomeOf({ allowed: true, data: { data: { vbo: null } } }, VBO), 'not-owner')
})

test('a refusal by the PDP leaves ownership unconfirmed', () => {
  assert.equal(outcomeOf({ allowed: false }, VBO), 'unconfirmed')
})

test('an answer IR cannot read releases nothing', () => {
  assert.equal(outcomeOf({ allowed: true, data: {} }, VBO), 'unconfirmed')
  assert.equal(outcomeOf({ allowed: true, data: { data: { vbo: { vboId: 'other' } } } }, VBO), 'unconfirmed')
})

test('reads the consent id from the query and the token from the fragment', () => {
  const back = readPortalReturn('?status=ok&consent_id=c-1', '#consent_token=aaa.bbb.ccc')
  assert.deepEqual(back, { status: 'ok', consentId: 'c-1', consentToken: 'aaa.bbb.ccc' })
})

test('a refused consent comes back as denied', () => {
  assert.deepEqual(readPortalReturn('?status=denied', ''), { status: 'denied' })
})

test('a return without a token is invalid', () => {
  assert.deepEqual(readPortalReturn('?status=ok&consent_id=c-1', ''), { status: 'invalid' })
})
