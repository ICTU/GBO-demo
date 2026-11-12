import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import { runtimeConfigValue } from '../src/hooks/redirectValidation.ts'

const root = new URL('../', import.meta.url)

test('loads allowed return origins from container runtime configuration', async () => {
  const [dockerfile, html, template] = await Promise.all([
    readFile(new URL('Dockerfile', root), 'utf8'),
    readFile(new URL('index.html', root), 'utf8'),
    readFile(new URL('runtime-config.js.template', root), 'utf8'),
  ])

  assert.match(dockerfile, /COPY runtime-config\.js\.template \/etc\/nginx\/templates\/runtime-config\.js\.template/)
  assert.match(html, /<script src="\/runtime-config\.js"><\/script>/)
  assert.match(template, /allowedReturnOrigins: "\$\{ALLOWED_RETURN_ORIGINS\}"/)
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
