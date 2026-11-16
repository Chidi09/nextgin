import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import { buildManifest, prerenderEligibility } from '../dist/manifest.js'

// Real onBuildComplete context captured from Next.js 16.4.6 (static, ISR,
// dynamic+Suspense, server action, middleware, a header rule and a redirect).
const ctx = JSON.parse(fs.readFileSync(new URL('./fixtures/probe-build-context.json', import.meta.url), 'utf8'))
const { manifest, copies } = buildManifest(ctx, { workerDir: 'server' })

test('fully static pages are served by Go', () => {
  const paths = manifest.prerendered.map((p) => p.pathname).sort()
  assert.deepEqual(paths, ['/', '/action', '/static'])
  const home = manifest.prerendered.find((p) => p.pathname === '/')
  assert.equal(home.file, 'prerendered/index.html')
  assert.equal(home.status, 200)
  assert.equal(home.headers['content-type'], 'text/html; charset=utf-8')
  assert.equal(home.headers['cache-control'], 's-maxage=31536000')
  assert.equal(home.headers['x-next-cache-tags'], undefined, 'internal header must be stripped')
})

test('ISR, internal and RSC sibling outputs are left to the worker', () => {
  const isr = manifest.skippedPrerenders.find((p) => p.pathname === '/isr')
  assert.match(isr.reason, /revalidate=30/)
  assert.ok(manifest.skippedPrerenders.some((p) => p.pathname === '/_global-error'))
  assert.ok(!manifest.prerendered.some((p) => p.pathname.endsWith('.rsc')))
  assert.equal(prerenderEligibility({ pathname: '/x.rsc' }), 'not a primary output (rsc/segment sibling)')
})

test('static files carry cache headers; error pages are excluded', () => {
  const chunk = manifest.staticFiles.find((f) => f.pathname.startsWith('/_next/static/chunks/'))
  assert.equal(chunk.headers['cache-control'], 'public,max-age=31536000,immutable')
  assert.equal(chunk.immutable, true)
  assert.ok(chunk.file.startsWith('static/_next/static/chunks/'))
  assert.ok(!manifest.staticFiles.some((f) => f.pathname === '/404' || f.pathname === '/500'))
})

test('bypass rules classify headers, redirects and middleware', () => {
  const kinds = manifest.bypass.map((r) => `${r.kind}:${r.source}`)
  assert.deepEqual(kinds, ['headers:/static', 'redirect:/:path+/', 'redirect:/old', 'middleware:/dyn/:path*'])
  const hdr = manifest.bypass[0]
  assert.equal(hdr.applicable, true)
  assert.deepEqual(hdr.headers, { 'x-custom': '1' })
})

test('every manifest file has a copy op', () => {
  const targets = new Set(copies.map((c) => c.to))
  for (const e of [...manifest.staticFiles, ...manifest.prerendered]) assert.ok(targets.has(e.file), e.file)
  assert.equal(manifest.worker.entry, 'server/nextgin-worker.cjs')
})

test('public files are added (build outputs win on conflict)', () => {
  const { manifest: m, copies: c } = buildManifest(ctx, {
    workerDir: 'server',
    publicFiles: [
      { relPath: 'robots.txt', filePath: '/project/public/robots.txt' },
      { relPath: 'img/a b.png', filePath: '/project/public/img/a b.png' },
    ],
  })
  const robots = m.staticFiles.find((f) => f.pathname === '/robots.txt')
  assert.equal(robots.file, 'static/robots.txt')
  assert.equal(robots.headers['cache-control'], 'public, max-age=0')
  assert.ok(m.staticFiles.some((f) => f.pathname === '/img/a b.png'), 'pathnames are stored decoded')
  assert.ok(c.some((x) => x.to === 'static/robots.txt'))
})
