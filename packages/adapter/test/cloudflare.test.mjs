import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { pathToFileURL } from 'node:url'
import {
  CF_BACKEND_BINDING,
  CF_COMPAT_DATE,
  CF_GENERATED_MARKER,
  CF_INTERNAL_ORIGIN,
  canOverwrite,
  generateBackendWrangler,
  generateWebWorker,
  generateWebWrangler,
  serializeWrangler,
  workerName,
} from '../dist/cloudflare.js'
import { goPrefixes } from '../dist/prefixes.js'

test('workerName normalizes, truncates, and rejects empty results', () => {
  assert.equal(workerName('My App!'), 'my-app')
  assert.equal(workerName('--x--'), 'x')

  const long = 'a'.repeat(53) + '-bbbbbb'
  assert.equal(long.length, 60)
  const truncated = workerName(long)
  assert.ok(truncated.length <= 54)
  assert.equal(truncated.endsWith('-'), false)
  assert.equal(truncated, 'a'.repeat(53))

  assert.throws(() => workerName('!!!'), /invalid worker name/)
})

test('generateBackendWrangler deduplicates cloudflare crons in first-seen order', () => {
  const exp = {
    version: 1,
    routes: [{ method: 'GET', path: '/api/ping' }],
    crons: [
      {
        name: 'cleanup',
        schedule: '*/5 * * * *',
        cloudflare: '*/5 * * * *',
        path: '/_nextgin/cron/cleanup',
        runsPerDayMax: 288,
      },
      {
        name: 'weekday',
        schedule: '0 9 * * 1-5',
        cloudflare: '0 9 * * 2,3,4,5,6',
        path: '/_nextgin/cron/weekday',
        runsPerDayMax: 1,
      },
      {
        name: 'cleanup-two',
        schedule: '*/5 * * * *',
        cloudflare: '*/5 * * * *',
        path: '/_nextgin/cron/cleanup-two',
        runsPerDayMax: 288,
      },
    ],
  }

  const cfg = generateBackendWrangler(exp, { name: 'my-app' })
  assert.deepStrictEqual(cfg, {
    name: 'my-app-backend',
    main: 'build/worker.mjs',
    compatibility_date: CF_COMPAT_DATE,
    workers_dev: false,
    preview_urls: false,
    triggers: {
      crons: ['*/5 * * * *', '0 9 * * 2,3,4,5,6'],
    },
  })
  assert.ok(serializeWrangler(cfg).includes('"workers_dev": false'))
})

test('generateWebWrangler builds expected service bindings, assets, and vars', () => {
  assert.deepStrictEqual(generateWebWrangler({ name: 'my-app' }), {
    name: 'my-app',
    main: 'nextgin-worker.mjs',
    compatibility_date: CF_COMPAT_DATE,
    compatibility_flags: ['nodejs_compat'],
    assets: {
      directory: '.open-next/assets',
      binding: 'ASSETS',
    },
    services: [
      { binding: 'WORKER_SELF_REFERENCE', service: 'my-app' },
      { binding: CF_BACKEND_BINDING, service: 'my-app-backend' },
    ],
    vars: {
      NEXTGIN_INTERNAL_URL: CF_INTERNAL_ORIGIN,
    },
  })
})

test('serializeWrangler prepends marker line and valid formatted JSON', () => {
  const input = { name: 'my-app', main: 'nextgin-worker.mjs' }
  const out = serializeWrangler(input)
  const firstNewline = out.indexOf('\n')
  assert.equal(out.slice(0, firstNewline), CF_GENERATED_MARKER)
  assert.deepStrictEqual(JSON.parse(out.slice(firstNewline + 1)), input)
})

test('generateWebWorker routes Go prefixes, patches globalThis.fetch, and re-exports OpenNext symbols', async () => {
  const source = generateWebWorker(['api', 'webhooks'])
  assert.ok(source.startsWith(CF_GENERATED_MARKER + '\n'))
  assert.ok(source.includes('const GO_PREFIXES = ["api","webhooks"];'))

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'nextgin-cf-test-'))
  const originalFetch = globalThis.fetch
  try {
    fs.mkdirSync(path.join(tmpDir, '.open-next'), { recursive: true })
    fs.writeFileSync(
      path.join(tmpDir, '.open-next', 'worker.js'),
      'export default { fetch: () => new Response("next") };\nexport class DOQueueHandler {}\n',
      'utf8',
    )
    const workerPath = path.join(tmpDir, 'nextgin-worker.mjs')
    fs.writeFileSync(workerPath, source, 'utf8')

    const mod = await import(pathToFileURL(workerPath).href)
    assert.equal(typeof mod.DOQueueHandler, 'function')

    const backendCalls = []
    const env = {
      [CF_BACKEND_BINDING]: {
        fetch: async (input, init) => {
          const url = typeof input === 'string' ? input : input.url
          backendCalls.push({ url, init })
          return new Response(`go:${url}`)
        },
      },
    }

    const goRes = await mod.default.fetch(new Request('https://example.com/api/x'), env, {})
    assert.equal(await goRes.text(), 'go:https://example.com/api/x')
    assert.equal(backendCalls.length, 1)

    const nextRes = await mod.default.fetch(new Request('https://example.com/about'), env, {})
    assert.equal(await nextRes.text(), 'next')
    assert.equal(backendCalls.length, 1)

    const internalRes = await globalThis.fetch(`${CF_INTERNAL_ORIGIN}/api/y`)
    assert.equal(await internalRes.text(), `go:${CF_INTERNAL_ORIGIN}/api/y`)
    assert.equal(backendCalls.length, 2)
  } finally {
    globalThis.fetch = originalFetch
    fs.rmSync(tmpDir, { recursive: true, force: true })
  }
})

test('canOverwrite checks undefined and generated marker first line', () => {
  assert.equal(canOverwrite(undefined), true)
  assert.equal(canOverwrite(`${CF_GENERATED_MARKER}\n{}`), true)
  assert.equal(canOverwrite('// custom file\n{}'), false)
})

test('goPrefixes validates routes and returns sorted unique prefixes', () => {
  assert.deepStrictEqual(
    goPrefixes({
      version: 1,
      routes: [
        { method: 'POST', path: '/webhooks/stripe' },
        { method: 'GET', path: '/api/todos' },
        { method: 'GET', path: '/api/health' },
      ],
      crons: [],
    }),
    ['api', 'webhooks'],
  )

  assert.throws(
    () => goPrefixes({ version: 1, routes: [{ method: 'GET', path: '/' }], crons: [] }),
    /nextgin: route GET \/ has no static first segment; Vercel needs a path prefix to route it to Go/,
  )
  assert.throws(
    () => goPrefixes({ version: 1, routes: [{ method: 'GET', path: '/:slug' }], crons: [] }),
    /nextgin: route GET \/:slug has no static first segment; Vercel needs a path prefix to route it to Go/,
  )
  assert.throws(
    () => goPrefixes({ version: 1, routes: [{ method: 'GET', path: '/_next/data' }], crons: [] }),
    /reserved prefix "_next"/,
  )
})

test('cf-backend-worker.mjs template uses persistent binding and omits per-event run()', () => {
  const templatePath = new URL('../templates/cf-backend-worker.mjs', import.meta.url)
  const content = fs.readFileSync(templatePath, 'utf8')
  for (const needle of [
    'createRuntimeContext',
    'handleRequest',
    'runScheduler',
    'handleQueueMessageBatch',
    'v0.36.0',
    'go.run(',
  ]) {
    assert.ok(content.includes(needle), `expected template to include ${needle}`)
  }
  assert.equal(content.includes('await run('), false)
  assert.ok(content.includes('currentGo.exited'), 'expected template to check currentGo.exited')
  assert.ok(content.includes('__nextginCronErrors'), 'expected template to read __nextginCronErrors')
  assert.ok(content.includes('throw new Error(msg)'), 'expected template to throw new Error(msg)')
  assert.equal(content.includes('runPromise'), false)
})

test('generateBackendWrangler with customDomain adds custom_domain route and keeps workers_dev/preview_urls false', () => {
  const exp = {
    version: 1,
    routes: [{ method: 'GET', path: '/api/ping' }],
    crons: [
      {
        name: 'cleanup',
        schedule: '*/5 * * * *',
        cloudflare: '*/5 * * * *',
        path: '/_nextgin/cron/cleanup',
        runsPerDayMax: 288,
      },
    ],
  }

  const cfg = generateBackendWrangler(exp, { name: 'my-app', customDomain: 'api.shop.com' })
  assert.deepStrictEqual(cfg, {
    name: 'my-app-backend',
    main: 'build/worker.mjs',
    compatibility_date: CF_COMPAT_DATE,
    workers_dev: false,
    preview_urls: false,
    routes: [{ pattern: 'api.shop.com', custom_domain: true }],
    triggers: {
      crons: ['*/5 * * * *'],
    },
  })
})
