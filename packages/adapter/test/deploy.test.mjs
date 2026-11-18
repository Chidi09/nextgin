import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  DEPLOY_CONFIG_FILE,
  parseDeployConfig,
  serializeDeployConfig,
  validateDomain,
  validateHost,
  validateName,
  validateSshPort,
} from '../dist/deploy/config.js'
import { detectLayout, detectServices } from '../dist/deploy/detect.js'
import {
  mergeEnv,
  newSecret,
  parseEnv,
  resetService,
  serializeEnv,
} from '../dist/deploy/env.js'
import {
  GENERATED_MARKER,
  canOverwriteGenerated,
  dbNameFor,
  generateCaddyfile,
  generateCompose,
  generateDockerfile,
} from '../dist/deploy/generate.js'

test('validateHost accepts valid hosts and rejects unsafe inputs', () => {
  assert.equal(validateHost('root@203.0.113.7'), 'root@203.0.113.7')
  assert.equal(validateHost('deploy@vps.example.com'), 'deploy@vps.example.com')
  assert.equal(validateHost('vps'), 'vps')
  assert.equal(validateHost('2001:db8::1'), '2001:db8::1')
  assert.equal(validateHost('root@2001:db8::1'), 'root@2001:db8::1')

  for (const bad of [
    '-oProxyCommand=x',
    'a b',
    'a;b',
    '$(x)',
    '',
    '-x@y',
    '@vps',
    'a@b@c',
    '[::1]',
    'root@[::1]',
  ]) {
    assert.throws(() => validateHost(bad), /nextgin deploy: invalid --host/)
  }
})

test('validateName normalizes, truncates, and rejects empty results', () => {
  assert.equal(validateName('My_App! 01'), 'my-app-01')
  assert.equal(validateName('---hello---world---'), 'hello-world')
  assert.equal(validateName('a'.repeat(50)), 'a'.repeat(40))
  assert.equal(validateName(`${'a'.repeat(39)}--b`), 'a'.repeat(39))

  assert.throws(() => validateName('---'), /nextgin deploy: invalid --name/)
  assert.throws(() => validateName(''), /nextgin deploy: invalid --name/)
})

test('validateDomain lowercases and rejects invalid domains', () => {
  assert.equal(validateDomain('Example.COM'), 'example.com')
  assert.equal(validateDomain('sub.api-1.example.co.uk'), 'sub.api-1.example.co.uk')

  for (const bad of [
    'https://example.com',
    'example.com:8080',
    'example.com/path',
    '*.example.com',
    '-bad.example.com',
    'localhost',
    '',
  ]) {
    assert.throws(() => validateDomain(bad), /nextgin deploy: invalid --domain/)
  }
})

test('validateSshPort accepts 1..65535 and numeric strings, rejects invalid values', () => {
  assert.equal(validateSshPort(22), 22)
  assert.equal(validateSshPort('2222'), 2222)
  assert.equal(validateSshPort(1), 1)
  assert.equal(validateSshPort(65535), 65535)

  for (const bad of [0, -1, 65536, 22.5, '0', '65536', '22abc', '', null, undefined]) {
    assert.throws(() => validateSshPort(bad), /nextgin deploy: invalid --ssh-port/)
  }
})

test('parseDeployConfig and serializeDeployConfig round trip and reject unknown or secret keys', () => {
  assert.equal(DEPLOY_CONFIG_FILE, 'nextgin.deploy.json')

  const serialized = serializeDeployConfig({
    sshPort: 2222,
    domain: 'app.example.com',
    name: 'my-app',
    host: 'deploy@203.0.113.7',
  })

  assert.equal(
    serialized,
    `{
  "host": "deploy@203.0.113.7",
  "name": "my-app",
  "domain": "app.example.com",
  "sshPort": 2222
}
`,
  )

  assert.deepStrictEqual(parseDeployConfig(serialized), {
    host: 'deploy@203.0.113.7',
    name: 'my-app',
    domain: 'app.example.com',
    sshPort: 2222,
  })

  assert.equal(serializeDeployConfig({ host: 'vps', domain: undefined }), '{\n  "host": "vps"\n}\n')

  assert.throws(() => parseDeployConfig('{bad json'), /invalid nextgin\.deploy\.json/)
  assert.throws(() => parseDeployConfig('[]'), /must be a JSON object/)
  assert.throws(
    () => parseDeployConfig('{"host":"vps","extra":"x"}'),
    /unknown key "extra" in nextgin\.deploy\.json/,
  )
  assert.throws(
    () => parseDeployConfig('{"host":"vps","databaseUrl":"postgres://secret"}'),
    /unknown key "databaseUrl" in nextgin\.deploy\.json/,
  )
  assert.throws(
    () => parseDeployConfig('{"password":"secret"}'),
    /unknown key "password" in nextgin\.deploy\.json/,
  )
})

test('detectServices parses single-line and block requires and ignores indirect and comments', () => {
  assert.deepStrictEqual(
    detectServices(`
module example.com/app

go 1.25

require github.com/jackc/pgx/v5 v5.7.1
`),
    { postgres: true, redis: false },
  )

  assert.deepStrictEqual(
    detectServices(`
module example.com/app

require (
  // github.com/lib/pq v1.10.9
  github.com/gin-gonic/gin v1.10.0
  gorm.io/driver/postgres v1.5.9
  github.com/redis/go-redis/v9 v9.7.0
)

require (
  github.com/gomodule/redigo v1.9.2 // indirect
)
`),
    { postgres: true, redis: true },
  )

  assert.deepStrictEqual(
    detectServices(`
module example.com/app

require (
  github.com/jackc/pgx/v5 v5.7.1 // indirect
  github.com/redis/go-redis/v9 v9.7.0 // indirect
)
`),
    { postgres: false, redis: false },
  )

  assert.deepStrictEqual(
    detectServices(`
module example.com/app

require github.com/gin-gonic/gin v1.10.0
`),
    { postgres: false, redis: false },
  )
})

test('detectLayout detects split, root, split-wins, and throws when neither matches', () => {
  const base = mkdtempSync(join(tmpdir(), 'nextgin-detect-'))
  try {
    const splitDir = join(base, 'split')
    mkdirSync(join(splitDir, 'web'), { recursive: true })
    mkdirSync(join(splitDir, 'backend'), { recursive: true })
    writeFileSync(join(splitDir, 'web', 'package.json'), '{}')
    writeFileSync(join(splitDir, 'backend', 'go.mod'), 'module app\n')
    assert.deepStrictEqual(detectLayout(splitDir), {
      kind: 'split',
      webDir: 'web',
      backendDir: 'backend',
    })

    const rootDir = join(base, 'root')
    mkdirSync(rootDir, { recursive: true })
    writeFileSync(join(rootDir, 'package.json'), '{}')
    writeFileSync(join(rootDir, 'go.mod'), 'module app\n')
    assert.deepStrictEqual(detectLayout(rootDir), {
      kind: 'root',
      webDir: '.',
      backendDir: '.',
    })

    const bothDir = join(base, 'both')
    mkdirSync(join(bothDir, 'web'), { recursive: true })
    mkdirSync(join(bothDir, 'backend'), { recursive: true })
    writeFileSync(join(bothDir, 'web', 'package.json'), '{}')
    writeFileSync(join(bothDir, 'backend', 'go.mod'), 'module app\n')
    writeFileSync(join(bothDir, 'package.json'), '{}')
    writeFileSync(join(bothDir, 'go.mod'), 'module app\n')
    assert.deepStrictEqual(detectLayout(bothDir), {
      kind: 'split',
      webDir: 'web',
      backendDir: 'backend',
    })

    const emptyDir = join(base, 'empty')
    mkdirSync(emptyDir, { recursive: true })
    assert.throws(
      () => detectLayout(emptyDir),
      /expected web\/package\.json \+ backend\/go\.mod, or package\.json \+ go\.mod/,
    )
  } finally {
    rmSync(base, { recursive: true, force: true })
  }
})

test('parseEnv and serializeEnv round trip including values with = and CRLF', () => {
  const input = [
    '# comment',
    '',
    '  ',
    'DATABASE_URL=postgres://u:p=1@host:5432/db?sslmode=disable\r',
    'QUOTED="hello world"  ',
    'INVALID-KEY=ignored',
    'NO_EQUALS_LINE',
  ].join('\n')

  const parsed = parseEnv(input)
  assert.equal(parsed.get('DATABASE_URL'), 'postgres://u:p=1@host:5432/db?sslmode=disable')
  assert.equal(parsed.get('QUOTED'), '"hello world"  ')
  assert.equal(parsed.has('INVALID-KEY'), false)
  assert.equal(parsed.size, 2)

  const serialized = serializeEnv(parsed)
  assert.equal(
    serialized,
    '# managed by nextgin; existing values are never overwritten\n' +
      'DATABASE_URL=postgres://u:p=1@host:5432/db?sslmode=disable\n' +
      'QUOTED="hello world"  \n',
  )
  assert.deepStrictEqual(parseEnv(serialized), parsed)
})

test('newSecret returns 48 hex chars', () => {
  const s = newSecret()
  assert.match(s, /^[0-9a-f]{48}$/)
})

test('mergeEnv handles internal, external, none, idempotency, and immutability', () => {
  let i = 0
  const detGen = () => `secret_${++i}`

  const existing = new Map([['CUSTOM_KEY', 'keep']])
  const merged = mergeEnv(
    existing,
    { postgres: 'internal', redis: 'internal', dbName: 'my_app' },
    detGen,
  )

  assert.equal(existing.size, 1)
  assert.equal(existing.has('DATABASE_URL'), false)

  assert.equal(merged.get('CUSTOM_KEY'), 'keep')
  assert.equal(merged.get('NEXTGIN_POSTGRES'), 'internal')
  assert.equal(merged.get('NEXTGIN_REDIS'), 'internal')
  assert.equal(merged.get('POSTGRES_PASSWORD'), 'secret_1')
  assert.equal(merged.get('DATABASE_URL'), 'postgres://nextgin:secret_1@postgres:5432/my_app?sslmode=disable')
  assert.equal(merged.get('REDIS_PASSWORD'), 'secret_2')
  assert.equal(merged.get('REDIS_URL'), 'redis://:secret_2@redis:6379/0')

  const mergedTwice = mergeEnv(
    merged,
    { postgres: 'internal', redis: 'internal', dbName: 'other_db' },
    () => 'should_not_be_called',
  )
  assert.deepStrictEqual(mergedTwice, merged)

  const ext = mergeEnv(new Map(), {
    postgres: 'external',
    redis: 'external',
    dbName: 'my_app',
    externalPostgresUrl: 'postgres://ext/db',
    externalRedisUrl: 'redis://ext/0',
  })
  assert.equal(ext.get('DATABASE_URL'), 'postgres://ext/db')
  assert.equal(ext.get('REDIS_URL'), 'redis://ext/0')
  assert.equal(ext.has('POSTGRES_PASSWORD'), false)
  assert.equal(ext.has('REDIS_PASSWORD'), false)

  assert.throws(
    () => mergeEnv(new Map(), { postgres: 'external', redis: 'none', dbName: 'my_app' }),
    /nextgin deploy: external Postgres URL is required/,
  )
  assert.throws(
    () => mergeEnv(new Map(), { postgres: 'none', redis: 'external', dbName: 'my_app' }),
    /nextgin deploy: external Redis URL is required/,
  )

  const noneEnv = mergeEnv(new Map(), { postgres: 'none', redis: 'none', dbName: 'my_app' })
  assert.deepStrictEqual(
    noneEnv,
    new Map([
      ['NEXTGIN_POSTGRES', 'none'],
      ['NEXTGIN_REDIS', 'none'],
    ]),
  )
})

test('resetService removes mode and URL while keeping passwords', () => {
  const env = new Map([
    ['NEXTGIN_POSTGRES', 'internal'],
    ['POSTGRES_PASSWORD', 'pgpw'],
    ['DATABASE_URL', 'postgres://nextgin:pgpw@postgres:5432/app?sslmode=disable'],
    ['NEXTGIN_REDIS', 'internal'],
    ['REDIS_PASSWORD', 'rdpw'],
    ['REDIS_URL', 'redis://:rdpw@redis:6379/0'],
  ])

  const afterPg = resetService(env, 'postgres')
  assert.equal(afterPg.has('NEXTGIN_POSTGRES'), false)
  assert.equal(afterPg.has('DATABASE_URL'), false)
  assert.equal(afterPg.get('POSTGRES_PASSWORD'), 'pgpw')
  assert.equal(afterPg.get('REDIS_URL'), 'redis://:rdpw@redis:6379/0')

  const afterRedis = resetService(afterPg, 'redis')
  assert.equal(afterRedis.has('NEXTGIN_REDIS'), false)
  assert.equal(afterRedis.has('REDIS_URL'), false)
  assert.equal(afterRedis.get('REDIS_PASSWORD'), 'rdpw')
})

test('generateDockerfile matches exact specification', () => {
  const expected = `# generated by nextgin — edits are overwritten; remove this line to take ownership
FROM node:22-bookworm-slim
WORKDIR /app
COPY --chown=node:node server /app/server
COPY --chown=node:node .nextgin /app/.nextgin
ENV PORT=8080
USER node
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=5 CMD node -e "fetch('http://127.0.0.1:8080/_nextgin/health').then(r=>process.exit(r.status<500?0:1)).catch(()=>process.exit(1))"
ENTRYPOINT ["/app/server"]
`
  assert.equal(generateDockerfile(), expected)
})

test('generateCompose handles all service and domain combinations', () => {
  // 1. internal + internal + no domain
  const c1 = generateCompose({
    name: 'shop',
    image: 'nextgin-shop:latest',
    postgres: 'internal',
    redis: 'internal',
    dbName: 'shop',
  })
  assert.equal(c1.split('\n')[0], GENERATED_MARKER)
  assert.match(c1, /^name: nextgin-shop$/m)
  assert.match(c1, /ports: \["80:8080"\]/)
  assert.match(c1, /depends_on:\n\s+postgres:\n\s+condition: service_healthy\n\s+redis:\n\s+condition: service_healthy/)
  assert.match(c1, /^  postgres:/m)
  assert.match(c1, /^  redis:/m)
  assert.doesNotMatch(c1, /^  caddy:/m)
  assert.match(c1, /^volumes:\n  pgdata:\n  redisdata:\n$/m)
  // No ports under postgres or redis
  const pgSection = c1.slice(c1.indexOf('\n  postgres:'), c1.indexOf('\n  redis:'))
  const redisSection = c1.slice(c1.indexOf('\n  redis:'), c1.indexOf('\nvolumes:'))
  assert.doesNotMatch(pgSection, /ports:/)
  assert.doesNotMatch(redisSection, /ports:/)

  // 2. internal postgres only with domain
  const c2 = generateCompose({
    name: 'shop',
    image: 'nextgin-shop:latest',
    postgres: 'internal',
    redis: 'none',
    domain: 'shop.example.com',
    dbName: 'shop',
  })
  assert.equal(c2.split('\n')[0], GENERATED_MARKER)
  assert.doesNotMatch(c2, /80:8080/)
  assert.match(c2, /depends_on:\n\s+postgres:\n\s+condition: service_healthy/)
  assert.match(c2, /^  postgres:/m)
  assert.doesNotMatch(c2, /^  redis:/m)
  assert.match(c2, /^  caddy:/m)
  assert.match(c2, /ports: \["80:80", "443:443"\]/)
  assert.match(c2, /^volumes:\n  pgdata:\n  caddy_data:\n  caddy_config:\n$/m)

  // 3. external + external with domain
  const c3 = generateCompose({
    name: 'shop',
    image: 'nextgin-shop:latest',
    postgres: 'external',
    redis: 'external',
    domain: 'shop.example.com',
    dbName: 'shop',
  })
  assert.doesNotMatch(c3, /80:8080/)
  assert.doesNotMatch(c3, /^  postgres:/m)
  assert.doesNotMatch(c3, /^  redis:/m)
  assert.match(c3, /^  caddy:/m)
  assert.match(c3, /^volumes:\n  caddy_data:\n  caddy_config:\n$/m)
  // app should not have depends_on
  const appSection3 = c3.slice(c3.indexOf('  app:'), c3.indexOf('  caddy:'))
  assert.doesNotMatch(appSection3, /depends_on:/)

  // 4. none + none no domain
  const c4 = generateCompose({
    name: 'shop',
    image: 'nextgin-shop:latest',
    postgres: 'none',
    redis: 'none',
    dbName: 'shop',
  })
  assert.match(c4, /ports: \["80:8080"\]/)
  assert.doesNotMatch(c4, /depends_on:/)
  assert.doesNotMatch(c4, /^  postgres:/m)
  assert.doesNotMatch(c4, /^  redis:/m)
  assert.doesNotMatch(c4, /^  caddy:/m)
  assert.doesNotMatch(c4, /^volumes:/m)
})

test('generateCaddyfile, canOverwriteGenerated, and dbNameFor', () => {
  assert.equal(
    generateCaddyfile('app.example.com'),
    `${GENERATED_MARKER}\napp.example.com {\n\treverse_proxy app:8080\n}\n`,
  )

  assert.equal(canOverwriteGenerated(undefined), true)
  assert.equal(canOverwriteGenerated(`${GENERATED_MARKER}\nFROM node:22\n`), true)
  assert.equal(canOverwriteGenerated('# custom\nFROM node:22\n'), false)

  assert.equal(dbNameFor('My-App'), 'my_app')
  assert.equal(dbNameFor('123app'), 'app_123app')
  assert.equal(dbNameFor('a'.repeat(50)), 'a'.repeat(40))
  assert.equal(dbNameFor(''), 'app')
})

test('generateCompose supports custom httpPort and httpsPort', () => {
  const noDomain = generateCompose({
    name: 'shop',
    image: 'nextgin-shop:latest',
    postgres: 'none',
    redis: 'none',
    dbName: 'shop',
    httpPort: 8081,
    httpsPort: 8443,
  })
  assert.match(noDomain, /ports: \["8081:8080"\]/)

  const withDomain = generateCompose({
    name: 'shop',
    image: 'nextgin-shop:latest',
    postgres: 'none',
    redis: 'none',
    domain: 'shop.example.com',
    dbName: 'shop',
    httpPort: 8081,
    httpsPort: 8443,
  })
  assert.match(withDomain, /ports: \["8081:80", "8443:443"\]/)
})

test('mergeEnv doubles $ in external Postgres and Redis URLs for compose', () => {
  const ext = mergeEnv(new Map(), {
    postgres: 'external',
    redis: 'external',
    dbName: 'my_app',
    externalPostgresUrl: 'postgres://user:p$ss$word@db.example.com:5432/app',
    externalRedisUrl: 'redis://:r$d$s@redis.example.com:6379/0',
  })
  assert.equal(ext.get('DATABASE_URL'), 'postgres://user:p$$ss$$word@db.example.com:5432/app')
  assert.equal(ext.get('REDIS_URL'), 'redis://:r$$d$$s@redis.example.com:6379/0')
})
