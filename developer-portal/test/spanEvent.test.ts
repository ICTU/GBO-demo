import { test } from 'node:test'
import assert from 'node:assert/strict'
import { asSpanInfo, type SpanEvent } from '../src/util/spanEvent.ts'

function span(attributes: Record<string, string>, status_code = 0): SpanEvent {
  return {
    trace_id: 't', span_id: 's', service: 'eudi-adapter', name: 'POST',
    start_nanos: 0, end_nanos: 0, status_code, attributes,
  }
}

test('legacy attributes still give path and error', () => {
  const info = asSpanInfo(span({ 'http.target': '/api/dvtp/query', 'http.status_code': '502' }))
  assert.equal(info.httpPath, '/api/dvtp/query')
  assert.equal(info.error, true)
})

test('url.path is used as the request path', () => {
  const info = asSpanInfo(span({ 'url.path': '/pid/', 'url.full': 'http://eudi-adapter/pid/' }))
  assert.equal(info.httpPath, '/pid/')
})

test('url.full is the last path fallback', () => {
  assert.equal(asSpanInfo(span({ 'url.full': 'http://x/y' })).httpPath, 'http://x/y')
})

test('http.response.status_code >= 400 marks the span as an error', () => {
  assert.equal(asSpanInfo(span({ 'http.response.status_code': '500' })).error, true)
  assert.equal(asSpanInfo(span({ 'http.response.status_code': '400' })).error, true)
})

test('http.response.status_code 200 is not an error', () => {
  assert.equal(asSpanInfo(span({ 'http.response.status_code': '200' })).error, false)
})

test('OTel status ERROR stays an error regardless of HTTP status', () => {
  assert.equal(asSpanInfo(span({ 'http.response.status_code': '200' }, 2)).error, true)
})
