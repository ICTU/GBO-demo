import { test } from 'node:test'
import assert from 'node:assert/strict'
import { isSpanError, type JaegerSpan } from '../src/api/jaegerClient.ts'

function span(tags: JaegerSpan['tags']): JaegerSpan {
  return { tags } as JaegerSpan
}

test('legacy http.status_code >= 400 is an error', () => {
  assert.equal(isSpanError(span([{ key: 'http.status_code', value: 503, type: 'int64' }])), true)
})

test('http.response.status_code >= 400 is an error', () => {
  assert.equal(isSpanError(span([{ key: 'http.response.status_code', value: 500, type: 'int64' }])), true)
})

test('http.response.status_code 200 is not an error', () => {
  assert.equal(isSpanError(span([{ key: 'http.response.status_code', value: 200, type: 'int64' }])), false)
})

test('otel.status_code ERROR is still an error', () => {
  assert.equal(isSpanError(span([{ key: 'otel.status_code', value: 'ERROR', type: 'string' }])), true)
})
