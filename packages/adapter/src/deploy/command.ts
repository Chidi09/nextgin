import { spawnSync } from 'node:child_process'
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'
import {
  DEPLOY_CONFIG_FILE,
  parseDeployConfig,
  serializeDeployConfig,
  validateDomain,
  validateHost,
  validateName,
  validateSshPort,
  type DeployConfig,
} from './config.js'
import { detectLayout, detectServices } from './detect.js'
import { mergeEnv, parseEnv, resetService, serializeEnv, type DbMode } from './env.js'
import {
  canOverwriteGenerated,
  dbNameFor,
  generateCaddyfile,
  generateCompose,
  generateDockerfile,
} from './generate.js'
import { createPrompter, type Prompter } from './prompt.js'
import {
  createLocalTransport,
  createSshTransport,
  shQuote,
  type Transport,
} from './transport.js'

export interface BuildFns {
  nextBuild(webAbs: string): void
  goBuild(backendAbs: string, outFile: string, arch: 'amd64' | 'arm64'): void
  dockerBuild(ctxDir: string, tags: string[], arch: string): void
  nativeModules(dir: string): string[]
}

export interface DeployDeps {
  cwd: string
  transport?: Transport
  prompter?: Prompter
  build?: BuildFns
  log?: (s: string) => void
  env?: NodeJS.ProcessEnv
  sleep?: (ms: number) => Promise<void>
}

const DIR_RE = /^\/[A-Za-z0-9._/-]*$/
const SECRET_ENV_KEYS = new Set([
  'POSTGRES_PASSWORD',
  'REDIS_PASSWORD',
  'DATABASE_URL',
  'REDIS_URL',
])

function validateDir(raw: unknown): string {
  if (typeof raw !== 'string' || !DIR_RE.test(raw) || raw.split('/').includes('..')) {
    throw new Error(`nextgin deploy: invalid --dir "${String(raw)}"`)
  }
  return raw.length > 1 && raw.endsWith('/') ? raw.slice(0, -1) : raw
}

function validatePortFlag(raw: unknown, flagName: string): number {
  let port: number
  if (typeof raw === 'number') {
    port = raw
  } else if (typeof raw === 'string' && /^\d+$/.test(raw)) {
    port = Number(raw)
  } else {
    throw new Error(`nextgin deploy: invalid --${flagName}`)
  }
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error(`nextgin deploy: invalid --${flagName}`)
  }
  return port
}

function validateServiceFlag(raw: unknown, flagName: string): DbMode {
  if (raw !== 'internal' && raw !== 'external' && raw !== 'none') {
    throw new Error(`nextgin deploy: invalid --${flagName} "${String(raw)}" (expected internal, external, or none)`)
  }
  return raw
}

function localGoArch(): 'amd64' | 'arm64' | string {
  if (process.arch === 'x64') return 'amd64'
  if (process.arch === 'arm64') return 'arm64'
  return process.arch
}

function utcTimestamp(d: Date = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return (
    String(d.getUTCFullYear()) +
    pad(d.getUTCMonth() + 1) +
    pad(d.getUTCDate()) +
    pad(d.getUTCHours()) +
    pad(d.getUTCMinutes()) +
    pad(d.getUTCSeconds())
  )
}

function findNodeFiles(dir: string): string[] {
  if (!existsSync(dir)) return []
  const found: string[] = []
  const stack = [dir]
  while (stack.length > 0) {
    const cur = stack.pop()!
    for (const entry of readdirSync(cur, { withFileTypes: true })) {
      const full = join(cur, entry.name)
      if (entry.isDirectory()) {
        stack.push(full)
      } else if (entry.isFile() && entry.name.endsWith('.node')) {
        found.push(full)
      }
    }
  }
  return found
}

function defaultBuildFns(): BuildFns {
  return {
    nextBuild(webAbs: string): void {
      const r = spawnSync(
        process.execPath,
        [process.argv[1], 'build', '--target', 'selfhost', '--no-go'],
        {
          cwd: webAbs,
          stdio: 'inherit',
          env: { ...process.env, NEXTGIN_TARGET: 'selfhost' },
        },
      )
      if (r.error) throw new Error(`nextgin deploy: next build failed: ${r.error.message}`)
      if (r.status !== 0) throw new Error(`nextgin deploy: next build exited with status ${r.status ?? 1}`)
    },

    goBuild(backendAbs: string, outFile: string, arch: 'amd64' | 'arm64'): void {
      const r = spawnSync(
        'go',
        ['build', '-trimpath', '-ldflags=-s -w', '-o', outFile, '.'],
        {
          cwd: backendAbs,
          stdio: 'inherit',
          env: {
            ...process.env,
            CGO_ENABLED: '0',
            GOOS: 'linux',
            GOARCH: arch,
          },
        },
      )
      if (r.error) throw new Error(`nextgin deploy: go build failed: ${r.error.message}`)
      if (r.status !== 0) throw new Error(`nextgin deploy: go build exited with status ${r.status ?? 1}`)
    },

    dockerBuild(ctxDir: string, tags: string[], arch: string): void {
      const args = ['build', '--platform', `linux/${arch}`]
      for (const tag of tags) {
        args.push('-t', tag)
      }
      args.push(ctxDir)
      const r = spawnSync('docker', args, { stdio: 'inherit' })
      if (r.error) throw new Error(`nextgin deploy: docker build failed: ${r.error.message}`)
      if (r.status !== 0) throw new Error(`nextgin deploy: docker build exited with status ${r.status ?? 1}`)
    },

    nativeModules(dir: string): string[] {
      return findNodeFiles(join(dir, '.nextgin', 'server'))
    },
  }
}

function isDebianOrUbuntu(osRelease: string): boolean {
  let id = ''
  let idLike = ''
  for (const line of osRelease.split(/\r?\n/)) {
    const trimmed = line.trim()
    if (trimmed.startsWith('ID=')) {
      id = trimmed.slice(3).replace(/^["']|["']$/g, '').toLowerCase()
    } else if (trimmed.startsWith('ID_LIKE=')) {
      idLike = trimmed.slice(8).replace(/^["']|["']$/g, '').toLowerCase()
    }
  }
  const tokens = `${id} ${idLike}`.split(/\s+/).filter(Boolean)
  return tokens.includes('debian') || tokens.includes('ubuntu')
}

function parseRemoteArch(raw: string): 'amd64' | 'arm64' {
  const m = raw.trim()
  if (m === 'x86_64') return 'amd64'
  if (m === 'aarch64' || m === 'arm64') return 'arm64'
  throw new Error(`nextgin deploy: unsupported architecture "${m}" (expected x86_64 or aarch64)`)
}

/**
 * Runs the `nextgin deploy` workflow.
 */
export async function runDeploy(
  flags: Record<string, string | boolean>,
  deps: DeployDeps,
): Promise<void> {
  const cwd = deps.cwd
  const log = deps.log ?? ((s: string) => console.log(s))
  const env = deps.env ?? process.env
  const prompter = deps.prompter ?? createPrompter()
  const build = deps.build ?? defaultBuildFns()
  const sleep = deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)))

  // 1. Options & validation
  const configPath = join(cwd, DEPLOY_CONFIG_FILE)
  const savedConfig: DeployConfig = existsSync(configPath)
    ? parseDeployConfig(readFileSync(configPath, 'utf8'))
    : {}

  const local = flags.local === true
  const hasHostFlag = flags.host !== undefined
  if (hasHostFlag && typeof flags.host !== 'string') {
    throw new Error('nextgin deploy: invalid --host')
  }
  if (local && hasHostFlag) {
    throw new Error('nextgin deploy: --host and --local are mutually exclusive')
  }

  const rawHost = local ? undefined : (flags.host as string | undefined) ?? savedConfig.host
  if (!local && !rawHost) {
    throw new Error('nextgin deploy: specify --host <user@host> or --local')
  }
  const host = rawHost !== undefined ? validateHost(rawHost) : undefined

  const rawName =
    flags.name !== undefined
      ? (typeof flags.name === 'string' ? flags.name : '')
      : savedConfig.name ?? basename(cwd)
  const name = validateName(rawName)

  const rawDomain = flags.domain !== undefined ? flags.domain : savedConfig.domain
  const domain = rawDomain !== undefined ? validateDomain(rawDomain as string) : undefined

  const sshPort =
    flags['ssh-port'] !== undefined
      ? validateSshPort(flags['ssh-port'])
      : validateSshPort(savedConfig.sshPort ?? 22)

  let identity: string | undefined
  if (flags.identity !== undefined) {
    if (typeof flags.identity !== 'string' || flags.identity.length === 0 || flags.identity.startsWith('-')) {
      throw new Error(`nextgin deploy: invalid --identity "${String(flags.identity)}"`)
    }
    identity = flags.identity
  }

  const dir = flags.dir !== undefined ? validateDir(flags.dir) : '/opt/nextgin'
  const httpPort = flags['http-port'] !== undefined ? validatePortFlag(flags['http-port'], 'http-port') : 80
  const httpsPort = flags['https-port'] !== undefined ? validatePortFlag(flags['https-port'], 'https-port') : 443
  if (httpPort === httpsPort) {
    throw new Error('nextgin deploy: --http-port and --https-port must be different')
  }

  const postgresFlag =
    flags.postgres !== undefined ? validateServiceFlag(flags.postgres, 'postgres') : undefined
  const redisFlag =
    flags.redis !== undefined ? validateServiceFlag(flags.redis, 'redis') : undefined

  const reconfigure = flags.reconfigure === true
  const dryRun = flags['dry-run'] === true
  const installDocker = flags['install-docker'] !== false

  // 2. Layout & service detection
  const layout = detectLayout(cwd)
  const webAbs = resolve(cwd, layout.webDir)
  const backendAbs = resolve(cwd, layout.backendDir)
  const goMod = readFileSync(join(backendAbs, 'go.mod'), 'utf8')
  const detected = detectServices(goMod)

  // 3-5. Remote preflight (skipped in --dry-run)
  let transport: Transport | undefined
  let isRoot = false
  let dockerCmd = 'docker'
  let arch: 'amd64' | 'arm64'
  const remoteDir = dir === '/' ? `/${name}` : `${dir}/${name}`
  const qRemoteDir = shQuote(remoteDir)
  let existingEnv = new Map<string, string>()

  if (dryRun) {
    const assumed = localGoArch()
    if (assumed !== 'amd64' && assumed !== 'arm64') {
      throw new Error(`nextgin deploy: unsupported local architecture "${assumed}"`)
    }
    arch = assumed
  } else {
    transport =
      deps.transport ??
      (local
        ? createLocalTransport()
        : createSshTransport({ host: host!, port: sshPort, identity }))

    if (transport.kind === 'ssh') {
      const sshCheck = transport.exec('echo ok')
      if (sshCheck.status !== 0 || sshCheck.stdout.trim() !== 'ok') {
        const detail = sshCheck.stderr.trim() || `exit code ${sshCheck.status}`
        throw new Error(`nextgin deploy: SSH check failed for ${transport.label}: ${detail}`)
      }

      const osRes = transport.exec('cat /etc/os-release')
      if (osRes.status !== 0 || !isDebianOrUbuntu(osRes.stdout)) {
        throw new Error(`nextgin deploy: unsupported OS on ${transport.label} (expected Debian or Ubuntu)`)
      }

      const uidRes = transport.exec('id -u')
      isRoot = uidRes.status === 0 && uidRes.stdout.trim() === '0'
      if (!isRoot) {
        const sudoRes = transport.exec('sudo -n true')
        if (sudoRes.status !== 0) {
          throw new Error(
            `nextgin deploy: remote user on ${transport.label} must be root or have passwordless sudo`,
          )
        }
      }

      const dockerInfo = transport.exec('docker info >/dev/null 2>&1')
      dockerCmd = dockerInfo.status === 0 || isRoot ? 'docker' : 'sudo -n docker'

      const composeCheck = transport.exec(`${dockerCmd} compose version`)
      if (composeCheck.status !== 0) {
        if (!installDocker) {
          throw new Error(
            `nextgin deploy: Docker Compose is not installed on ${transport.label}; install Docker (https://get.docker.com) or omit --no-install-docker`,
          )
        }
        log(`[nextgin] installing Docker on ${transport.label}...`)
        const installCmd = isRoot
          ? 'curl -fsSL https://get.docker.com | sh'
          : 'curl -fsSL https://get.docker.com | sudo -n sh'
        const installRes = transport.exec(installCmd)
        if (installRes.status !== 0) {
          const detail = installRes.stderr.trim() || `exit code ${installRes.status}`
          throw new Error(`nextgin deploy: Docker installation failed: ${detail}`)
        }
        const postInfo = transport.exec('docker info >/dev/null 2>&1')
        dockerCmd = postInfo.status === 0 || isRoot ? 'docker' : 'sudo -n docker'
        const recheck = transport.exec(`${dockerCmd} compose version`)
        if (recheck.status !== 0) {
          throw new Error('nextgin deploy: Docker Compose still unavailable after installation')
        }
      }
    } else {
      const composeCheck = transport.exec('docker compose version')
      if (composeCheck.status !== 0) {
        throw new Error('nextgin deploy: Docker Compose is not available on this machine')
      }
    }

    const unameRes = transport.exec('uname -m')
    if (unameRes.status !== 0) {
      throw new Error(`nextgin deploy: failed to detect architecture: ${unameRes.stderr.trim()}`)
    }
    arch = parseRemoteArch(unameRes.stdout)

    const mkdirScript =
      transport.kind === 'local' || isRoot
        ? `mkdir -p ${qRemoteDir}`
        : `sudo -n mkdir -p ${qRemoteDir} && sudo -n chown "$(id -u):$(id -g)" ${qRemoteDir}`
    const mkdirRes = transport.exec(mkdirScript)
    if (mkdirRes.status !== 0) {
      throw new Error(`nextgin deploy: failed to create ${remoteDir}: ${mkdirRes.stderr.trim()}`)
    }

    const remoteEnvText = transport.readFile(`${remoteDir}/.env`)
    if (remoteEnvText !== undefined) {
      existingEnv = parseEnv(remoteEnvText)
    }
  }

  // 6. Decide service modes
  const resolveService = async (
    svc: 'postgres' | 'redis',
    isDetected: boolean,
    flagMode: DbMode | undefined,
  ): Promise<{ mode: DbMode; externalUrl?: string }> => {
    const envKey = svc === 'postgres' ? 'NEXTGIN_POSTGRES' : 'NEXTGIN_REDIS'
    const urlEnvVar = svc === 'postgres' ? 'DATABASE_URL' : 'REDIS_URL'
    const label = svc === 'postgres' ? 'Postgres' : 'Redis'

    const previousMode = existingEnv.get(envKey)
    if (reconfigure) {
      existingEnv = resetService(existingEnv, svc)
    }

    const savedMode = existingEnv.get(envKey)
    let mode: DbMode

    if (savedMode !== undefined) {
      if (flagMode !== undefined && flagMode !== savedMode) {
        throw new Error(
          `nextgin deploy: ${label} is already configured as ${savedMode}; pass --reconfigure to change it`,
        )
      }
      mode = savedMode as DbMode
    } else if (!isDetected) {
      mode = 'none'
    } else if (flagMode !== undefined) {
      mode = flagMode
    } else if (prompter.interactive) {
      const internal = await prompter.confirm(`${label} detected. Create one on this server?`, true)
      mode = internal ? 'internal' : 'external'
    } else {
      mode = 'internal'
    }

    if (reconfigure && previousMode === 'internal' && mode === 'external') {
      const volName = svc === 'postgres' ? 'pgdata' : 'redisdata'
      log(
        `[nextgin] warning: ${label} switched to external; existing volume nextgin-${name}_${volName} was left untouched`,
      )
    }

    let externalUrl: string | undefined
    if (mode === 'external' && !existingEnv.has(urlEnvVar)) {
      const fromEnv = env[urlEnvVar]
      if (fromEnv && fromEnv.length > 0) {
        externalUrl = fromEnv
      } else if (prompter.interactive) {
        externalUrl = await prompter.secret(`External ${label} URL`)
        if (!externalUrl) {
          throw new Error(`nextgin deploy: external ${label} URL is required`)
        }
      } else {
        throw new Error(`nextgin deploy: external ${label} URL is required (set ${urlEnvVar})`)
      }
    }

    return { mode, externalUrl }
  }

  const pg = await resolveService('postgres', detected.postgres, postgresFlag)
  const rd = await resolveService('redis', detected.redis, redisFlag)

  // 7. Merge env
  const dbName = dbNameFor(name)
  const mergedEnv = mergeEnv(existingEnv, {
    postgres: pg.mode,
    redis: rd.mode,
    dbName,
    externalPostgresUrl: pg.externalUrl,
    externalRedisUrl: rd.externalUrl,
  })

  // 8. Generate stack files
  const imageTag = `nextgin-${name}:${utcTimestamp()}`
  const latestTag = `nextgin-${name}:latest`
  const dockerfile = generateDockerfile()
  const compose = generateCompose({
    name,
    image: imageTag,
    postgres: pg.mode,
    redis: rd.mode,
    domain,
    dbName,
    httpPort,
    httpsPort,
  })
  const caddyfile = domain !== undefined ? generateCaddyfile(domain) : undefined

  if (dryRun) {
    const previewDir = join(cwd, '.nextgin', 'deploy')
    mkdirSync(previewDir, { recursive: true })
    writeFileSync(join(previewDir, 'Dockerfile'), dockerfile, 'utf8')
    writeFileSync(join(previewDir, 'docker-compose.yml'), compose, 'utf8')
    if (caddyfile !== undefined) {
      writeFileSync(join(previewDir, 'Caddyfile'), caddyfile, 'utf8')
    }
    const previewEnv = new Map<string, string>()
    for (const [k, v] of mergedEnv.entries()) {
      previewEnv.set(k, SECRET_ENV_KEYS.has(k) ? '<hidden>' : v)
    }
    writeFileSync(join(previewDir, 'env.preview'), serializeEnv(previewEnv), 'utf8')

    log('[nextgin] dry-run plan:')
    log(`[nextgin]   layout: ${layout.kind} (web: ${layout.webDir}, backend: ${layout.backendDir})`)
    log(`[nextgin]   services: postgres=${pg.mode}, redis=${rd.mode}`)
    log(`[nextgin]   arch: ${arch} (assumed local)`)
    log(`[nextgin]   wrote preview files to ${join('.nextgin', 'deploy')}`)
    return
  }

  // 9. Build in temp context dir
  const ctxDir = mkdtempSync(join(tmpdir(), 'nextgin-deploy-'))
  try {
    build.nextBuild(webAbs)
    const serverBin = join(ctxDir, 'server')
    build.goBuild(backendAbs, serverBin, arch)
    chmodSync(serverBin, 0o755)
    cpSync(join(webAbs, '.nextgin'), join(ctxDir, '.nextgin'), { recursive: true })
    writeFileSync(join(ctxDir, 'Dockerfile'), dockerfile, 'utf8')

    const nativeMods = build.nativeModules(ctxDir)
    const hostArch = localGoArch()
    if (nativeMods.length > 0 && hostArch !== arch) {
      throw new Error(
        `nextgin deploy: native modules (*.node) found in .nextgin/server (${nativeMods.join(', ')}); cannot cross-build from ${hostArch} to ${arch}`,
      )
    }

    build.dockerBuild(ctxDir, [imageTag, latestTag], arch)
  } finally {
    rmSync(ctxDir, { recursive: true, force: true })
  }

  // 10. Upload image and stack files
  await transport!.loadImage(imageTag, dockerCmd)

  const composePath = `${remoteDir}/docker-compose.yml`
  const existingCompose = transport!.readFile(composePath)
  if (canOverwriteGenerated(existingCompose)) {
    transport!.put(composePath, compose, '0644')
  } else {
    log(`[nextgin] warning: ${composePath} was modified; keeping existing file`)
  }

  if (caddyfile !== undefined) {
    const caddyPath = `${remoteDir}/Caddyfile`
    const existingCaddy = transport!.readFile(caddyPath)
    if (canOverwriteGenerated(existingCaddy)) {
      transport!.put(caddyPath, caddyfile, '0644')
    } else {
      log(`[nextgin] warning: ${caddyPath} was modified; keeping existing file`)
    }
  }

  transport!.put(`${remoteDir}/.env`, serializeEnv(mergedEnv), '0600')

  // 11. Compose up
  const upRes = transport!.exec(`cd ${qRemoteDir} && ${dockerCmd} compose up -d --remove-orphans`)
  if (upRes.status !== 0) {
    const detail = upRes.stderr.trim() || upRes.stdout.trim() || `exit code ${upRes.status}`
    throw new Error(`nextgin deploy: docker compose up failed: ${detail}`)
  }

  // 12. Wait for healthcheck (up to 120s, poll every 3s)
  const deadline = Date.now() + 120_000
  while (true) {
    const psRes = transport!.exec(`cd ${qRemoteDir} && ${dockerCmd} compose ps -q app`)
    const containerId = psRes.stdout.trim()
    if (containerId) {
      const inspRes = transport!.exec(
        `${dockerCmd} inspect --format '{{.State.Health.Status}}' ${shQuote(containerId)}`,
      )
      const status = inspRes.stdout.trim()
      if (status === 'healthy') {
        break
      }
      if (status === 'unhealthy') {
        const logsRes = transport!.exec(`cd ${qRemoteDir} && ${dockerCmd} compose logs --tail 50 app`)
        if (logsRes.stdout) log(logsRes.stdout.trimEnd())
        if (logsRes.stderr) log(logsRes.stderr.trimEnd())
        throw new Error('nextgin deploy: app container became unhealthy')
      }
    }

    if (Date.now() >= deadline) {
      const logsRes = transport!.exec(`cd ${qRemoteDir} && ${dockerCmd} compose logs --tail 50 app`)
      if (logsRes.stdout) log(logsRes.stdout.trimEnd())
      if (logsRes.stderr) log(logsRes.stderr.trimEnd())
      throw new Error('nextgin deploy: timed out waiting for app container to become healthy')
    }

    await sleep(3000)
  }

  // 13. Success output, prune old images, save config
  let url: string
  if (domain !== undefined) {
    url = `https://${domain}${httpsPort !== 443 ? `:${httpsPort}` : ''}`
  } else {
    const urlHost = local
      ? '127.0.0.1'
      : host!.includes('@')
        ? host!.slice(host!.indexOf('@') + 1)
        : host!
    const portSuffix = httpPort !== 80 ? `:${httpPort}` : ''
    url = `http://${urlHost}${portSuffix}`
  }

  log(`[nextgin] deployed: ${url}`)
  if (domain !== undefined) {
    log(`[nextgin] note: ensure DNS for ${domain} points to this server for automatic HTTPS`)
  }
  log('[nextgin] note: Postgres is not backed up automatically')

  const imgRes = transport!.exec(`${dockerCmd} images ${shQuote(`nextgin-${name}`)} --format '{{.Tag}}'`)
  if (imgRes.status === 0) {
    const timestampTags = imgRes.stdout
      .split(/\r?\n/)
      .map((t) => t.trim())
      .filter((t) => t.length > 0 && t !== 'latest' && t !== '<none>')
      .sort((a, b) => b.localeCompare(a))
    for (const oldTag of timestampTags.slice(3)) {
      transport!.exec(`${dockerCmd} rmi ${shQuote(`nextgin-${name}:${oldTag}`)}`)
    }
  }

  const toSave: DeployConfig = {
    ...(local ? {} : { host }),
    name,
    ...(domain !== undefined ? { domain } : {}),
    ...(sshPort !== 22 ? { sshPort } : {}),
  }
  writeFileSync(configPath, serializeDeployConfig(toSave), 'utf8')
}
