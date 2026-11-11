import { test } from 'node:test'
import assert from 'node:assert/strict'
import { euWalletLinksFrom } from '../src/lib/euWalletLinks.ts'

test('D1: an offer with a login link gets both EU-wallet links', () => {
  const links = euWalletLinksFrom([
    { provider: 'belastingdienst', key: 'inkomensverklaring_2025', profiles: {
      eudi: { link: 'openid4vp://?x', login_link: 'openid-credential-offer://?y' },
    } },
  ])
  assert.deepEqual(links.get('belastingdienst/inkomensverklaring_2025'), { link: 'openid4vp://?x', loginLink: 'openid-credential-offer://?y' })
})

test('D2: an offer without a login link has the disclosure link only', () => {
  const links = euWalletLinksFrom([
    { provider: 'rvig', key: 'akte_van_overlijden', profiles: { eudi: { link: 'openid4vp://?z' } } },
    { provider: 'rvig', key: 'nl_only', profiles: { nl: { link: 'https://ul' } } },
  ])
  assert.deepEqual(links.get('rvig/akte_van_overlijden'), { link: 'openid4vp://?z', loginLink: '' })
  assert.equal(links.has('rvig/nl_only'), false)
})

test('an answer that is not a list gives no links', () => {
  assert.equal(euWalletLinksFrom({} as unknown).size, 0)
})
