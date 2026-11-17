import { test } from 'node:test'
import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { runDeploy } from '../dist/deploy/command.js'
import { parseEnv } from '../dist/deploy/env.js'
import { buildSshArgs, shQuote } from '../dist/deploy/transport.js'

function makeProjectDir({ goMod = 'module example.com/app\n\ngo 1.25\nrequire (\n\tgithub.com/jackc/pgx/v5 v5.7.1\n\tgithub.com/redis/go-redis/v9 v9.7.0\n)\n' } = {}) {
  const dir = mkdtempSync(join(tmpdir(), 'nextgin-deploy-cmd-'))
  writeFileSync(join(dir, 'package.json'), '{"name":"test-app"}\n')
  writeFileSync(join(dir, 'go.mod'), goMod)
  return dir
}

function makeFakeBuilds() {
  const calls = []
  const build = {
    nextBuild(webAbs) {
      calls.push({ fn: 'nextBuild', webAbs })
      mkdirSync(join(webAbs, '.nextgin', 'server'), { recursive: true })
    },
    goBuild(backendAbs, outFile, arch) {
      calls.push({ fn: 'goBuild', backendAbs, outFile, arch })
      writeFileSync(outFile, 'fake-binary')
    },
    dockerBuild(ctxDir, tags, arch) {
      calls.push({ fn: 'dockerBuild', ctxDir, tags, arch })
    },
    nativeModules() {
      return []
    },
  }
  return { build, calls }
}

function makeFakeTransport({ kind = 'ssh', label = 'deploy@vps.example.com', initialFiles = {}, arch = 'x86_64' } = {}) {
  const execs = []
  const puts = []
  const loadedImages = []
  const files = new Map(Object.entries(initialFiles))

  const transport = {
    kind,
    label,
    exec(script) {
      execs.push(script)
      if (script === 'echo ok') return { status: 0, stdout: 'ok\n', stderr: '' }
      if (script === 'cat /etc/os-release') return { status: 0, stdout: 'ID=ubuntu\nID_LIKE=debian\n', stderr: '' }
      if (script === 'id -u') return { status: 0, stdout: '0\n', stderr: '' }
      if (script === 'docker info >/dev/null 2>&1') return { status: 0, stdout: '', stderr: '' }
      if (script.includes('compose version')) return { status: 0, stdout: 'Docker Compose version v2.29.0\n', stderr: '' }
      if (script === 'uname -m') return { status: 0, stdout: `${arch}\n`, stderr: '' }
      if (script.includes('compose ps -q app')) return { status: 0, stdout: 'abc123container\n', stderr: '' }
      if (script.includes('inspect --format')) return { status: 0, stdout: 'healthy\n', stderr: '' }
      if (script.includes('images ') && script.includes('--format')) return { status: 0, stdout: 'latest\n20260101000000\n', stderr: '' }
      return { status: 0, stdout: '', stderr: '' }
    },
    put(remotePath, data, mode) {
      const content = typeof data === 'string' ? data : data.toString('utf8')
      puts.push({ remotePath, data: content, mode })
      files.set(remotePath, content)
    },
    readFile(remotePath) {
      return files.get(remotePath)
    },
    loadImage(ref) {
      loadedImages.push(ref)
    },
  }
  return { transport, execs, puts, loadedImages, files }
}

test('shQuote quotes single quotes, spaces, command substitutions, and newlines', () => {
  assert.equal(shQuote("a'b"), "'a'\\''b'")
  assert.equal(shQuote('hello world'), "'hello world'")
  assert.equal(shQuote('$(x)'), "'$(x)'")
  assert.equal(shQuote('line1\nline2'), "'line1\nline2'")
})

test('buildSshArgs builds exact argv array and rejects leading dashes', () => {
  assert.deepStrictEqual(
    buildSshArgs({ host: 'deploy@vps.example.com' }, 'echo ok'),
    [
      '-o',
      'BatchMode=yes',
      '-o',
      'ConnectTimeout=10',
      '-p',
      '22',
      'deploy@vps.example.com',
      '--',
      'sh',
      '-c',
      "'echo ok'",
    ],
  )

  assert.deepStrictEqual(
    buildSshArgs({ host: 'vps', port: 2222, identity: '/home/u/.ssh/id_ed25519' }, "echo 'hi'"),
    [
      '-o',
      'BatchMode=yes',
      '-o',
      'ConnectTimeout=10',
      '-p',
      '2222',
      '-i',
      '/home/u/.ssh/id_ed25519',
      'vps',
      '--',
      'sh',
      '-c',
      "'echo '\\''hi'\\'''",
    ],
  )

  assert.throws(() => buildSshArgs({ host: '-oProxyCommand=x' }, 'echo ok'), /invalid ssh host/)
  assert.throws(() => buildSshArgs({ host: 'vps', identity: '-bad' }, 'echo ok'), /invalid ssh identity/)
})

test('dry-run writes preview files and makes no transport or build calls', async () => {
  const cwd = makeProjectDir()
  try {
    const fake = makeFakeTransport()
    const { build, calls } = makeFakeBuilds()
    const logs = []

    await runDeploy(
      {
        host: 'deploy@vps.example.com',
        name: 'my-app',
        domain: 'app.example.com',
        'dry-run': true,
      },
      {
        cwd,
        transport: fake.transport,
        build,
        prompter: {
          interactive: false,
          async confirm() {
            throw new Error('should not prompt')
          },
          async secret() {
            throw new Error('should not prompt')
          },
        },
        log: (s) => logs.push(s),
      },
    )

    assert.equal(fake.execs.length, 0)
    assert.equal(fake.puts.length, 0)
    assert.equal(fake.loadedImages.length, 0)
    assert.equal(calls.length, 0)
    assert.equal(existsSync(join(cwd, 'nextgin.deploy.json')), false)

    const previewDir = join(cwd, '.nextgin', 'deploy')
    assert.equal(existsSync(join(previewDir, 'Dockerfile')), true)
    assert.equal(existsSync(join(previewDir, 'docker-compose.yml')), true)
    assert.equal(existsSync(join(previewDir, 'Caddyfile')), true)

    const envPreview = parseEnv(readFileSync(join(previewDir, 'env.preview'), 'utf8'))
    assert.equal(envPreview.get('NEXTGIN_POSTGRES'), 'internal')
    assert.equal(envPreview.get('NEXTGIN_REDIS'), 'internal')
    assert.equal(envPreview.get('POSTGRES_PASSWORD'), '<hidden>')
    assert.equal(envPreview.get('REDIS_PASSWORD'), '<hidden>')
    assert.equal(envPreview.get('DATABASE_URL'), '<hidden>')
    assert.equal(envPreview.get('REDIS_URL'), '<hidden>')
  } finally {
    rmSync(cwd, { recursive: true, force: true })
  }
})

test('first deploy interactive yes -> internal URLs, no db ports, safe shQuote, no down/-v, and second deploy preserves .env', async () => {
  const cwd = makeProjectDir()
  try {
    const fake1 = makeFakeTransport()
    const { build } = makeFakeBuilds()
    const confirmQuestions = []

    await runDeploy(
      {
        host: 'deploy@vps.example.com',
        name: 'My_App! 01',
        dir: '/srv/my-apps',
      },
      {
        cwd,
        transport: fake1.transport,
        build,
        prompter: {
          interactive: true,
          async confirm(q) {
            confirmQuestions.push(q)
            return true
          },
          async secret() {
            throw new Error('should not ask secret')
          },
        },
        log: () => {},
        sleep: async () => {},
      },
    )

    assert.equal(confirmQuestions.length, 2)
    assert.equal(fake1.loadedImages.length, 1)

    const remoteDir = '/srv/my-apps/my-app-01'
    const qRemoteDir = shQuote(remoteDir)

    // Assert dynamic dir is always shQuoted in remote scripts
    const mkdirExec = fake1.execs.find((s) => s.includes('mkdir -p'))
    assert.ok(mkdirExec && mkdirExec.includes(qRemoteDir))

    const composeUpExec = fake1.execs.find((s) => s.includes('compose up'))
    assert.ok(composeUpExec && composeUpExec.includes(qRemoteDir))
    assert.equal(composeUpExec.includes(' down'), false)
    assert.equal(composeUpExec.includes(' -v'), false)

    // No exec anywhere should run `down` or `-v`
    for (const script of fake1.execs) {
      assert.equal(script.includes(' down'), false)
      assert.equal(script.includes(' -v'), false)
    }

    const envPut = fake1.puts.find((p) => p.remotePath === `${remoteDir}/.env`)
    assert.ok(envPut)
    assert.equal(envPut.mode, '0600')
    const envMap = parseEnv(envPut.data)
    assert.equal(envMap.get('NEXTGIN_POSTGRES'), 'internal')
    assert.equal(envMap.get('NEXTGIN_REDIS'), 'internal')
    assert.match(envMap.get('DATABASE_URL'), /^postgres:\/\/nextgin:[0-9a-f]{48}@postgres:5432\/my_app_01\?sslmode=disable$/)
    assert.match(envMap.get('REDIS_URL'), /^redis:\/\/:[0-9a-f]{48}@redis:6379\/0$/)

    const composePut = fake1.puts.find((p) => p.remotePath === `${remoteDir}/docker-compose.yml`)
    assert.ok(composePut)
    assert.equal(composePut.mode, '0644')
    const pgSection = composePut.data.slice(composePut.data.indexOf('\n  postgres:'), composePut.data.indexOf('\n  redis:'))
    const rdSection = composePut.data.slice(composePut.data.indexOf('\n  redis:'), composePut.data.indexOf('\nvolumes:'))
    assert.doesNotMatch(pgSection, /ports:/)
    assert.doesNotMatch(rdSection, /ports:/)

    // Second deploy: existing .env returned by readFile -> passwords/URLs byte-identical, prompter never asked
    const fake2 = makeFakeTransport({
      initialFiles: {
        [`${remoteDir}/.env`]: envPut.data,
        [`${remoteDir}/docker-compose.yml`]: composePut.data,
      },
    })

    await runDeploy(
      { dir: '/srv/my-apps' },
      {
        cwd,
        transport: fake2.transport,
        build,
        prompter: {
          interactive: true,
          async confirm() {
            throw new Error('prompter should not be asked on second deploy')
          },
          async secret() {
            throw new Error('prompter should not be asked on second deploy')
          },
        },
        log: () => {},
        sleep: async () => {},
      },
    )

    const envPut2 = fake2.puts.find((p) => p.remotePath === `${remoteDir}/.env`)
    assert.ok(envPut2)
    assert.equal(envPut2.data, envPut.data)
  } finally {
    rmSync(cwd, { recursive: true, force: true })
  }
})

test('non-interactive without flags defaults to internal', async () => {
  const cwd = makeProjectDir()
  try {
    const fake = makeFakeTransport({ kind: 'local', label: 'this machine' })
    const { build } = makeFakeBuilds()

    await runDeploy(
      { local: true, name: 'app' },
      {
        cwd,
        transport: fake.transport,
        build,
        prompter: {
          interactive: false,
          async confirm() {
            throw new Error('should not confirm in non-interactive')
          },
          async secret() {
            throw new Error('should not prompt secret')
          },
        },
        log: () => {},
        sleep: async () => {},
      },
    )

    const envPut = fake.puts.find((p) => p.remotePath === '/opt/nextgin/app/.env')
    assert.ok(envPut)
    const envMap = parseEnv(envPut.data)
    assert.equal(envMap.get('NEXTGIN_POSTGRES'), 'internal')
    assert.equal(envMap.get('NEXTGIN_REDIS'), 'internal')
  } finally {
    rmSync(cwd, { recursive: true, force: true })
  }
})

test('external via env var doubles $ and omits postgres service', async () => {
  const cwd = makeProjectDir({
    goMod: 'module example.com/app\n\ngo 1.25\nrequire github.com/jackc/pgx/v5 v5.7.1\n',
  })
  try {
    const fake = makeFakeTransport()
    const { build } = makeFakeBuilds()

    await runDeploy(
      { host: 'deploy@vps.example.com', name: 'extapp', postgres: 'external' },
      {
        cwd,
        transport: fake.transport,
        build,
        prompter: {
          interactive: false,
          async confirm() {
            return true
          },
          async secret() {
            throw new Error('should not prompt secret when env var is set')
          },
        },
        env: { DATABASE_URL: 'postgres://u:p$ss$1@db.example.com:5432/app' },
        log: () => {},
        sleep: async () => {},
      },
    )

    const envPut = fake.puts.find((p) => p.remotePath === '/opt/nextgin/extapp/.env')
    assert.ok(envPut)
    const envMap = parseEnv(envPut.data)
    assert.equal(envMap.get('NEXTGIN_POSTGRES'), 'external')
    assert.equal(envMap.get('DATABASE_URL'), 'postgres://u:p$$ss$$1@db.example.com:5432/app')

    const composePut = fake.puts.find((p) => p.remotePath === '/opt/nextgin/extapp/docker-compose.yml')
    assert.ok(composePut)
    assert.doesNotMatch(composePut.data, /^  postgres:/m)
  } finally {
    rmSync(cwd, { recursive: true, force: true })
  }
})

test('--reconfigure re-asks and flag disagreeing with saved mode errors', async () => {
  const cwd = makeProjectDir({
    goMod: 'module example.com/app\n\ngo 1.25\nrequire github.com/jackc/pgx/v5 v5.7.1\n',
  })
  try {
    const { build } = makeFakeBuilds()
    const initialEnv = [
      'NEXTGIN_POSTGRES=internal',
      'POSTGRES_PASSWORD=oldpw',
      'DATABASE_URL=postgres://nextgin:oldpw@postgres:5432/app?sslmode=disable',
      'NEXTGIN_REDIS=none',
    ].join('\n')

    // Flag disagrees without --reconfigure -> error
    const fakeDisagree = makeFakeTransport({
      initialFiles: { '/opt/nextgin/app/.env': initialEnv },
    })
    await assert.rejects(
      () =>
        runDeploy(
          { host: 'deploy@vps.example.com', name: 'app', postgres: 'external' },
          { cwd, transport: fakeDisagree.transport, build, log: () => {}, sleep: async () => {} },
        ),
      /already configured as internal; pass --reconfigure/,
    )

    // --reconfigure re-asks and switches to external
    const fakeReconf = makeFakeTransport({
      initialFiles: { '/opt/nextgin/app/.env': initialEnv },
    })
    let askedConfirm = false
    let askedSecret = false
    await runDeploy(
      { host: 'deploy@vps.example.com', name: 'app', reconfigure: true },
      {
        cwd,
        transport: fakeReconf.transport,
        build,
        prompter: {
          interactive: true,
          async confirm() {
            askedConfirm = true
            return false
          },
          async secret() {
            askedSecret = true
            return 'postgres://ext/db'
          },
        },
        log: () => {},
        sleep: async () => {},
      },
    )
    assert.equal(askedConfirm, true)
    assert.equal(askedSecret, true)
    const envPut = fakeReconf.puts.find((p) => p.remotePath === '/opt/nextgin/app/.env')
    const envMap = parseEnv(envPut.data)
    assert.equal(envMap.get('NEXTGIN_POSTGRES'), 'external')
    assert.equal(envMap.get('DATABASE_URL'), 'postgres://ext/db')
  } finally {
    rmSync(cwd, { recursive: true, force: true })
  }
})

test('edited remote compose (marker removed) is not overwritten', async () => {
  const cwd = makeProjectDir({ goMod: 'module example.com/app\n\ngo 1.25\n' })
  try {
    const customCompose = '# custom compose owned by user\nservices:\n  app:\n    image: custom\n'
    const fake = makeFakeTransport({
      initialFiles: {
        '/opt/nextgin/app/docker-compose.yml': customCompose,
      },
    })
    const { build } = makeFakeBuilds()
    const logs = []

    await runDeploy(
      { host: 'deploy@vps.example.com', name: 'app' },
      {
        cwd,
        transport: fake.transport,
        build,
        prompter: {
          interactive: false,
          async confirm() {
            return true
          },
          async secret() {
            return ''
          },
        },
        log: (s) => logs.push(s),
        sleep: async () => {},
      },
    )

    assert.equal(fake.puts.some((p) => p.remotePath === '/opt/nextgin/app/docker-compose.yml'), false)
    assert.ok(logs.some((l) => l.includes('warning:') && l.includes('docker-compose.yml')))
  } finally {
    rmSync(cwd, { recursive: true, force: true })
  }
})

test('validation rejects missing host/local, hostile --host, invalid --dir', async () => {
  const cwd = makeProjectDir({ goMod: 'module example.com/app\n\ngo 1.25\n' })
  try {
    const { build } = makeFakeBuilds()

    await assert.rejects(
      () => runDeploy({}, { cwd, build }),
      /--host.*--local|--local.*--host/,
    )

    await assert.rejects(
      () => runDeploy({ host: '-oProxyCommand=x' }, { cwd, build }),
      /invalid --host/,
    )

    await assert.rejects(
      () => runDeploy({ local: true, dir: '../x' }, { cwd, build }),
      /invalid --dir/,
    )

    await assert.rejects(
      () => runDeploy({ local: true, dir: '/opt/../x' }, { cwd, build }),
      /invalid --dir/,
    )
  } finally {
    rmSync(cwd, { recursive: true, force: true })
  }
})
