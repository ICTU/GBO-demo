import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readDecision } from '../src/lib/decisionView.ts'

const trace = [{ rule: 'DVT0001', code: 'YEAR_NOT_COVERED', steps: [{ code: 'YEAR_NOT_COVERED', label: 'Years', expected: 'bd:ib:2024 in scopes', status: 'fail' }] }]

test('a refused field: what the consumer was told next to the field reason', () => {
  const view = readDecision({
    decision: false,
    context: {
      reason_user: { en: 'Field not permitted' },
      graphql: {
        client: { code: 'FIELD_NOT_PERMITTED' },
        admin: {
          code: 'FIELD_NOT_PERMITTED',
          schema_digest: 'sha256:abc',
          denied_fields: [{
            index: 1,
            path: ['ingeschrevenPersoon', 'heeftBelastingjaarAangifte'],
            key: 'IngeschrevenPersoon.heeftBelastingjaarAangifte',
            code: 'YEAR_NOT_COVERED',
            evaluated: ['DVT0001'],
            trace,
          }],
        },
      },
    },
  })
  assert.equal(view.shape, 'profile')
  assert.equal(view.allowed, false)
  assert.equal(view.consumerCode, 'FIELD_NOT_PERMITTED')
  assert.equal(view.adminCode, 'FIELD_NOT_PERMITTED')
  assert.equal(view.schemaDigest, 'sha256:abc')
  assert.deepEqual(view.denied, [{
    path: 'ingeschrevenPersoon.heeftBelastingjaarAangifte',
    key: 'IngeschrevenPersoon.heeftBelastingjaarAangifte',
    code: 'YEAR_NOT_COVERED',
    rules: trace,
  }])
})

test('an unverifiable request: the hidden and the real code, and the mapper message', () => {
  const view = readDecision({
    decision: false,
    context: {
      graphql: {
        client: { code: 'FIELD_NOT_PERMITTED' },
        admin: { code: 'COVERAGE_UNVERIFIABLE', subcode: 'INVALID_QUERY', message: 'Field Selections at 1:31', schema_digest: 'sha256:abc' },
      },
    },
  })
  assert.equal(view.consumerCode, 'FIELD_NOT_PERMITTED')
  assert.equal(view.adminCode, 'COVERAGE_UNVERIFIABLE INVALID_QUERY')
  assert.equal(view.mapperMessage, 'Field Selections at 1:31')
  assert.equal(view.denied, undefined)
})

test('a server problem: the consumer saw access denied', () => {
  const view = readDecision({
    decision: false,
    context: { graphql: { client: { code: 'ACCESS_DENIED' }, admin: { code: 'PIP_UNAVAILABLE', denied_fields: [] } } },
  })
  assert.equal(view.consumerCode, 'ACCESS_DENIED')
  assert.equal(view.adminCode, 'PIP_UNAVAILABLE')
  assert.deepEqual(view.denied, [])
})

test('an allow lists the granting rule per field', () => {
  const view = readDecision({
    decision: true,
    context: { granted: [{ field: 'ingeschrevenPersoon', key: 'Query.ingeschrevenPersoon', rule: 'DVT0001', steps: [] }] },
  })
  assert.equal(view.allowed, true)
  assert.deepEqual(view.granted, [{ path: 'ingeschrevenPersoon', key: 'Query.ingeschrevenPersoon', rules: [{ rule: 'DVT0001', steps: [] }] }])
})

test('a decision logged before the decision context still renders', () => {
  const view = readDecision({
    decision: false,
    context: {
      reason_admin: { code: 'YEAR_NOT_COVERED' },
      denied_fields: [{ field: 'ingeschrevenPersoon.heeftBelastingjaarAangifte', key: 'IngeschrevenPersoon.heeftBelastingjaarAangifte', code: 'YEAR_NOT_COVERED', evaluated: trace }],
    },
  })
  assert.equal(view.shape, 'gbo')
  assert.equal(view.adminCode, 'YEAR_NOT_COVERED')
  assert.equal(view.consumerCode, undefined)
  assert.equal(view.denied?.[0].code, 'YEAR_NOT_COVERED')
  assert.equal(view.denied?.[0].rules[0].rule, 'DVT0001')
})

test('the oldest cascade traces still render', () => {
  const view = readDecision({
    decision: false,
    context: { reason_admin: { code: 'CONSENT_WITHDRAWN', evaluated: [{ code: 'CONSENT_WITHDRAWN', status: 'fail' }] } },
  })
  assert.equal(view.shape, 'legacy')
  assert.deepEqual(view.legacyEvaluated, [{ code: 'CONSENT_WITHDRAWN', status: 'fail' }])
})

test('anything else renders as nothing to show', () => {
  for (const result of [undefined, null, {}, { decision: false, context: {} }]) {
    assert.equal(readDecision(result).shape, 'none')
  }
})
