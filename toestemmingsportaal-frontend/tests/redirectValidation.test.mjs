import assert from 'node:assert/strict'
import test from 'node:test'
import {
  parseAllowedReturnOrigins,
  validatedRedirectContext,
} from '../src/hooks/redirectValidation.ts'

const allowedOrigins = parseAllowedReturnOrigins('https://consumer.example,http://localhost:9001')
const validContext = {
  service: 'hypotheek',
  purpose: 'Aanvraag beoordelen',
  scope: ['bd:ib:2025'],
  client_oin: '00000001234567890000',
  client_name: 'Demo dienstverlener',
  valid_until: '',
  return_url: 'https://consumer.example/return?state=abc',
}

test('accepts an http or https return URL on the exact allowlisted origin', () => {
  const context = validatedRedirectContext(validContext, allowedOrigins)
  assert.equal(context?.return_url, 'https://consumer.example/return?state=abc')
})

test('rejects an external origin', () => {
  assert.equal(
    validatedRedirectContext({ ...validContext, return_url: 'https://evil.example/steal' }, allowedOrigins),
    null,
  )
})

test('rejects javascript URLs and embedded credentials', () => {
  assert.equal(
    validatedRedirectContext({ ...validContext, return_url: 'javascript:alert(1)' }, allowedOrigins),
    null,
  )
  assert.equal(
    validatedRedirectContext({ ...validContext, return_url: 'https://user:pass@consumer.example/return' }, allowedOrigins),
    null,
  )
})

test('revalidates a stored redirect context', () => {
  const stored = JSON.parse(JSON.stringify({
    ...validContext,
    return_url: 'https://evil.example/from-session-storage',
  }))
  assert.equal(validatedRedirectContext(stored, allowedOrigins), null)
})
