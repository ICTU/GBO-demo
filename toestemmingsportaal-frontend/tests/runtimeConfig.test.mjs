import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

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
