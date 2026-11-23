import assert from 'node:assert/strict'
import test from 'node:test'
import { outcomeOf } from '../src/lib/ownership.ts'

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

test('a null that comes with GraphQL errors is not a "not the owner"', () => {
  assert.equal(outcomeOf({ allowed: true, data: { data: { vbo: null }, errors: [{ message: 'boom' }] } }, VBO), 'unconfirmed')
})

test('an answer IR cannot read releases nothing', () => {
  assert.equal(outcomeOf({ allowed: true, data: {} }, VBO), 'unconfirmed')
  assert.equal(outcomeOf({ allowed: true, data: { data: { vbo: { vboId: 'other' } } } }, VBO), 'unconfirmed')
})
