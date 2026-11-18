import { test } from 'node:test'
import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { detectTarget } from '../dist/targets.js'
import {
  DOCKER_GENERATED_MARKER,
  canOverwriteDockerfile,
  generateDockerfile,
  generateVercelSplitConfig,
  parseGoVersion,
  validateBackendUrl,
  validateSplitFlags,
} from '../dist/split.js'

test('generateVercelSplitConfig builds rewrite and direct configs without crons or services', () => {
  const exp = {
    version: 1,
    routes: [
      { method: 'POST', path: '/webhooks/stripe' },
      { method: 'GET', path: '/api/health' },
      { method: 'GET', path: '/api/todos' },
    ],
    crons: [
      {
        name: 'cleanup',
        schedule: '*/5 * * * *',
        path: '/_nextgin/cron/cleanup',
        runsPerDayMax: 288,
      },
    ],
  }

  const rewriteCfg = generateVercelSplitConfig(exp, {
    backendUrl: 'https://api.shop.com',
    apiMode: 'rewrite',
  })
  assert.deepStrictEqual(rewriteCfg, {
    rewrites: [
      { source: '/api/:path*', destination: 'https://api.shop.com/api/:path*' },
      { source: '/webhooks/:path*', destination: 'https://api.shop.com/webhooks/:path*' },
    ],
  })
  assert.equal('crons' in rewriteCfg, false)
  assert.equal('services' in rewriteCfg, false)

  const directCfg = generateVercelSplitConfig(exp, {
    backendUrl: 'https://api.shop.com',
    apiMode: 'direct',
  })
  assert.deepStrictEqual(directCfg, {})
  assert.equal('crons' in directCfg, false)
  assert.equal('services' in directCfg, false)
})

test('validateBackendUrl accepts valid origins and rejects invalid URLs', () => {
  assert.equal(validateBackendUrl('https://api.shop.com', 'container'), 'https://api.shop.com')
  assert.equal(validateBackendUrl('https://api.shop.com/', 'container'), 'https://api.shop.com')
  assert.equal(validateBackendUrl('http://localhost:8080/', 'container'), 'http://localhost:8080')
  assert.equal(validateBackendUrl('http://127.0.0.1:8080', 'container'), 'http://127.0.0.1:8080')

  assert.throws(() => validateBackendUrl('', 'container'), /--backend-url is required/)
  assert.throws(() => validateBackendUrl(undefined, 'container'), /--backend-url is required/)
  assert.throws(() => validateBackendUrl('http://api.shop.com', 'container'), /must use https/)
  assert.throws(() => validateBackendUrl('https://api.shop.com/v1', 'container'), /must not include a path/)
  assert.throws(() => validateBackendUrl('https://api.shop.com/?a=1', 'container'), /must not include a query/)
  assert.throws(() => validateBackendUrl('https://api.shop.com?', 'container'), /must not include a query/)
  assert.throws(() => validateBackendUrl('https://user:pass@api.shop.com', 'container'), /must not include credentials/)
  assert.throws(() => validateBackendUrl('https://api.shop.com/#sec', 'container'), /must not include a hash/)
  assert.throws(() => validateBackendUrl('https://api.shop.com#', 'container'), /must not include a hash/)
})

test('validateSplitFlags validates all flag combinations and defaults', () => {
  assert.deepStrictEqual(
    validateSplitFlags({
      'web-target': 'vercel',
      'backend-target': 'container',
      'backend-url': 'https://api.shop.com/',
    }),
    {
      webTarget: 'vercel',
      backendTarget: 'container',
      backendUrl: 'https://api.shop.com',
      backendHostname: 'api.shop.com',
      apiMode: 'direct',
    },
  )

  assert.deepStrictEqual(
    validateSplitFlags({
      'web-target': 'vercel',
      'backend-target': 'container',
      'backend-url': 'http://localhost:8080/',
      'api-mode': 'rewrite',
    }),
    {
      webTarget: 'vercel',
      backendTarget: 'container',
      backendUrl: 'http://localhost:8080',
      backendHostname: 'localhost',
      apiMode: 'rewrite',
    },
  )

  assert.throws(
    () =>
      validateSplitFlags({
        target: 'vercel',
        'web-target': 'vercel',
        'backend-target': 'container',
        'backend-url': 'https://api.shop.com',
      }),
    /--target cannot be combined with --web-target\/--backend-target/,
  )

  assert.throws(
    () => validateSplitFlags({ 'web-target': 'vercel', 'backend-url': 'https://api.shop.com' }),
    /--backend-target/,
  )

  assert.throws(
    () => validateSplitFlags({ 'backend-target': 'container', 'backend-url': 'https://api.shop.com' }),
    /--web-target/,
  )

  assert.throws(
    () =>
      validateSplitFlags({
        'web-target': 'cloudflare',
        'backend-target': 'container',
        'backend-url': 'https://api.shop.com',
      }),
    /only vercel is supported as --web-target for now/,
  )

  assert.throws(
    () =>
      validateSplitFlags({
        'web-target': 'vercel',
        'backend-target': 'fly',
        'backend-url': 'https://api.shop.com',
      }),
    /--backend-target must be "container"/,
  )

  assert.throws(
    () =>
      validateSplitFlags({
        'web-target': 'vercel',
        'backend-target': 'container',
      }),
    /--backend-url is required/,
  )

  assert.throws(
    () =>
      validateSplitFlags({
        'web-target': 'vercel',
        'backend-target': 'container',
        'backend-url': 'https://api.shop.com',
        'api-mode': 'proxy',
      }),
    /--api-mode must be "direct" or "rewrite"/,
  )
})

test('generateDockerfile, parseGoVersion, and canOverwriteDockerfile', () => {
  const expected = `# generated by nextgin — edits are overwritten; remove this line to take ownership
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
ENV NEXTGIN_TARGET=backend
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/server"]
`
  assert.equal(generateDockerfile({ goVersion: '1.25' }), expected)

  assert.equal(parseGoVersion('module app\n\ngo 1.25.0\n'), '1.25')
  assert.equal(parseGoVersion('module app\n\ngo 1.24\n'), '1.24')
  assert.equal(parseGoVersion('module app\n\ngo 1.25.0\n\ntoolchain go1.25.1\n'), '1.25')
  assert.throws(() => parseGoVersion('module app\n'), /no `go <major>\.<minor>` directive/)

  assert.equal(canOverwriteDockerfile(undefined), true)
  assert.equal(canOverwriteDockerfile(`${DOCKER_GENERATED_MARKER}\nFROM golang:1.25 AS build\n`), true)
  assert.equal(canOverwriteDockerfile('# custom dockerfile\nFROM golang:1.25 AS build\n'), false)
})

test('detectTarget accepts backend', () => {
  assert.equal(detectTarget({ NEXTGIN_TARGET: 'backend' }), 'backend')
})

test('CLI smoke test: split build without --backend-url exits 1 with missing-flag message', () => {
  const cliPath = fileURLToPath(new URL('../dist/cli.js', import.meta.url))
  const res = spawnSync(
    process.execPath,
    [cliPath, 'build', '--web-target', 'vercel', '--backend-target', 'container'],
    { encoding: 'utf8' },
  )
  assert.equal(res.status, 1)
  assert.match(res.stderr, /--backend-url is required/)
})
