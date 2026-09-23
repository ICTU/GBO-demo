import assert from 'node:assert/strict'
import test from 'node:test'
import { requestedScopeGroups } from '../src/data/scopeGroups.ts'

test('returns only the requested scope groups, in the requested order', () => {
  const { groups, unknown } = requestedScopeGroups(['bd:ib:2024', 'bd:ib:2025'])
  assert.deepEqual(groups.map((g) => g.code), ['bd:ib:2024', 'bd:ib:2025'])
  assert.deepEqual(unknown, [])
})

test('a request for the LVG scope shows only that scope', () => {
  const { groups } = requestedScopeGroups(['lvg:vbo:eigendom'])
  assert.deepEqual(groups.map((g) => g.code), ['lvg:vbo:eigendom'])
})

test('reports a scope the portal cannot describe instead of dropping it', () => {
  const { groups, unknown } = requestedScopeGroups(['bd:ib:2025', 'x:onbekend'])
  assert.deepEqual(groups.map((g) => g.code), ['bd:ib:2025'])
  assert.deepEqual(unknown, ['x:onbekend'])
})

test('asks for a scope once, however often it was requested', () => {
  const { groups } = requestedScopeGroups(['bd:ib:2025', 'bd:ib:2025'])
  assert.equal(groups.length, 1)
})
