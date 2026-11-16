import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { detectTarget } from '../dist/targets.js'
import { parseExport } from '../dist/export.js'
import { generateVercelConfig, serializeVercelConfig } from '../dist/vercel.js'

const indexUrl = new URL('../dist/index.js', import.meta.url).href

test('detectTarget handles all branches without mutating process.env', () => {
  assert.equal(detectTarget({}), 'selfhost')
  assert.equal(detectTarget({ VERCEL: '1' }), 'vercel')
  assert.equal(detectTarget({ NEXTGIN_TARGET: '', VERCEL: '1' }), 'vercel')
  assert.equal(detectTarget({ NEXTGIN_TARGET: 'selfhost', VERCEL: '1' }), 'selfhost')
  assert.equal(detectTarget({ NEXTGIN_TARGET: 'vercel' }), 'vercel')
  assert.equal(detectTarget({ NEXTGIN_TARGET: 'cloudflare' }), 'cloudflare')
  assert.throws(() => detectTarget({ NEXTGIN_TARGET: 'fly' }), /nextgin: unknown NEXTGIN_TARGET "fly"/)
})

test('generateVercelConfig builds expected services, sorted rewrites, and crons', () => {
  const exp = {
    version: 1,
    routes: [
      { method: 'GET', path: '/api/health' },
      { method: 'GET', path: '/api/todos' },
      { method: 'PATCH', path: '/api/todos/:id' },
      { method: 'POST', path: '/webhooks/stripe' },
    ],
    crons: [
      {
        name: 'cleanup',
        schedule: '*/5 * * * *',
        cloudflare: '*/5 * * * *',
        path: '/_nextgin/cron/cleanup',
        runsPerDayMax: 288,
      },
      {
        name: 'digest',
        schedule: '0 9 * * 1',
        cloudflare: '0 9 * * 2',
        path: '/_nextgin/cron/digest',
        runsPerDayMax: 1,
      },
    ],
  }

  const cfg = generateVercelConfig(exp)
  assert.deepStrictEqual(cfg, {
    services: {
      web: {
        root: 'web/',
        framework: 'nextjs',
        bindings: [
          {
            type: 'service',
            service: 'backend',
            format: 'url',
            env: 'NEXTGIN_INTERNAL_URL',
          },
        ],
      },
      backend: {
        root: 'backend/',
        framework: 'go',
      },
    },
    rewrites: [
      { source: '/api/:path*', destination: { service: 'backend' } },
      { source: '/webhooks/:path*', destination: { service: 'backend' } },
      { source: '/_nextgin/:path*', destination: { service: 'backend' } },
      { source: '/(.*)', destination: { service: 'web' } },
    ],
    crons: [
      { path: '/_nextgin/cron/cleanup', schedule: '*/5 * * * *' },
      { path: '/_nextgin/cron/digest', schedule: '0 9 * * 1' },
    ],
  })
})

test('serializeVercelConfig preserves exact key order and omits crons when empty', () => {
  const cfg = generateVercelConfig(
    {
      version: 1,
      routes: [{ method: 'GET', path: '/api/ping' }],
      crons: [],
    },
    { webDir: './apps/web/', backendDir: './services/go/' },
  )
  assert.equal('crons' in cfg, false)

  const expected = `{
  "services": {
    "web": {
      "root": "apps/web/",
      "framework": "nextjs",
      "bindings": [
        {
          "type": "service",
          "service": "backend",
          "format": "url",
          "env": "NEXTGIN_INTERNAL_URL"
        }
      ]
    },
    "backend": {
      "root": "services/go/",
      "framework": "go"
    }
  },
  "rewrites": [
    {
      "source": "/api/:path*",
      "destination": {
        "service": "backend"
      }
    },
    {
      "source": "/_nextgin/:path*",
      "destination": {
        "service": "backend"
      }
    },
    {
      "source": "/(.*)",
      "destination": {
        "service": "web"
      }
    }
  ]
}
`
  assert.equal(serializeVercelConfig(cfg), expected)
})

test('generateVercelConfig rejects invalid routes, reserved prefixes, api dirs, and hobby sub-daily crons', () => {
  assert.throws(
    () => generateVercelConfig({ version: 1, routes: [{ method: 'GET', path: '/' }], crons: [] }),
    /nextgin: route GET \/ has no static first segment; Vercel needs a path prefix to route it to Go/,
  )
  assert.throws(
    () => generateVercelConfig({ version: 1, routes: [{ method: 'GET', path: '/:slug' }], crons: [] }),
    /nextgin: route GET \/:slug has no static first segment; Vercel needs a path prefix to route it to Go/,
  )
  assert.throws(
    () => generateVercelConfig({ version: 1, routes: [{ method: 'GET', path: '/_next/data' }], crons: [] }),
    /reserved prefix "_next"/,
  )
  assert.throws(
    () =>
      generateVercelConfig(
        { version: 1, routes: [{ method: 'GET', path: '/api/ping' }], crons: [] },
        { backendDir: 'api' },
      ),
    /cannot start with "api\/"/,
  )

  const subDaily = {
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
  assert.throws(
    () => generateVercelConfig(subDaily, { hobby: true }),
    /Vercel Hobby only allows daily cron jobs.*cleanup \(\*\/5 \* \* \* \*\)/,
  )

  const dailyOnly = {
    version: 1,
    routes: [{ method: 'GET', path: '/api/ping' }],
    crons: [
      {
        name: 'digest',
        schedule: '0 9 * * 1',
        cloudflare: '0 9 * * 2',
        path: '/_nextgin/cron/digest',
        runsPerDayMax: 1,
      },
    ],
  }
  assert.doesNotThrow(() => generateVercelConfig(dailyOnly, { hobby: true }))
})

test('parseExport rejects invalid versions and missing arrays', () => {
  assert.throws(
    () => parseExport(JSON.stringify({ version: 2, routes: [], crons: [] })),
    /^Error: nextgin: invalid export/,
  )
  assert.throws(
    () => parseExport(JSON.stringify({ version: 1, crons: [] })),
    /^Error: nextgin: invalid export/,
  )
  assert.throws(
    () => parseExport(JSON.stringify({ version: 1, routes: [] })),
    /^Error: nextgin: invalid export/,
  )
})

test('createNextGinAdapter returns undefined on vercel and adapter.js path on selfhost', () => {
  const script = `import { createNextGinAdapter } from ${JSON.stringify(indexUrl)}; console.log(String(createNextGinAdapter()));`

  const vercelRes = spawnSync(process.execPath, ['--input-type=module', '-e', script], {
    env: { ...process.env, NEXTGIN_TARGET: 'vercel' },
    encoding: 'utf8',
  })
  assert.equal(vercelRes.status, 0, vercelRes.stderr)
  assert.equal(vercelRes.stdout.trim(), 'undefined')

  const selfhostRes = spawnSync(process.execPath, ['--input-type=module', '-e', script], {
    env: { ...process.env, NEXTGIN_TARGET: 'selfhost' },
    encoding: 'utf8',
  })
  assert.equal(selfhostRes.status, 0, selfhostRes.stderr)
  const expectedAdapterPath = fileURLToPath(new URL('../dist/adapter.js', import.meta.url))
  assert.equal(selfhostRes.stdout.trim(), expectedAdapterPath)
  assert.ok(selfhostRes.stdout.trim().endsWith('adapter.js'))
})
