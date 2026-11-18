#!/usr/bin/env node
import { spawn, spawnSync, type ChildProcess } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { finalizeServer, readPending } from './finalize.js'
import { runExport } from './export.js'
import { detectTarget, type Target } from './targets.js'
import { generateVercelConfig, serializeVercelConfig } from './vercel.js'
import {
  canOverwriteDockerfile,
  generateDockerfile,
  generateVercelSplitConfig,
  parseGoVersion,
  validateSplitFlags,
} from './split.js'
import { runDeploy } from './deploy/command.js'
import { validateName } from './deploy/config.js'

const HELP = `nextgin — Next.js with a lightweight pure-Go API & cron backend

Usage:
  nextgin build [--target selfhost|vercel] [--out-dir .nextgin] [--go-out server] [--no-go] [--web web] [--backend backend] [--check] [--vercel-hobby]
      selfhost (default): runs \`next build\` (with @nextgin/adapter), finalizes
      .nextgin/server and compiles the Go binary from the current directory.
      vercel: exports Go routes/crons and writes vercel.json (or verifies it
      with --check; --vercel-hobby rejects sub-daily crons).

  nextgin build --web-target vercel --backend-target container --backend-url <url> [--api-mode direct|rewrite] [--web web] [--backend backend] [--name app] [--force] [--check]
      Split topology (Next.js on Vercel, Go backend on its own host; writes <web>/vercel.json and <backend>/Dockerfile).

  nextgin dev [--port 8080] [--next-port 3001]
      Runs \`next dev\` and the Go server side by side. Go proxies every
      non-API request (including HMR) to next dev and restarts on .go changes
      (uses \`air\` when installed).

  nextgin finalize [--out-dir .nextgin]
      Copies .next/standalone into .nextgin/server (normally automatic).

  nextgin deploy [--host user@host | --local] [--name app] [--domain example.com] [--postgres internal|external|none] [--redis internal|external|none] [--reconfigure] [--dry-run] [--no-install-docker] [--ssh-port 22] [--identity ~/.ssh/key] [--dir /opt/nextgin] [--http-port 80] [--https-port 443]
      Builds in self-host mode and deploys the app, Postgres, Redis, and
      Caddy (when --domain is set) to a VPS via SSH or locally (--local).
`

type Flags = Record<string, string | boolean>

function parseFlags(argv: string[]): Flags {
  const flags: Flags = {}
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i]
    if (!a.startsWith('--')) continue
    const [k, inline] = a.slice(2).split('=', 2)
    if (k.startsWith('no-')) flags[k.slice(3)] = false
    else if (inline !== undefined) flags[k] = inline
    else if (argv[i + 1] && !argv[i + 1].startsWith('--')) flags[k] = argv[++i]
    else flags[k] = true
  }
  return flags
}

const cwd = process.cwd()

function nextBin(): string {
  const req = createRequire(path.join(cwd, 'package.json'))
  try {
    return req.resolve('next/dist/bin/next')
  } catch {
    fail('could not resolve `next` from this directory; run nextgin inside your Next.js project')
  }
}

function fail(msg: string): never {
  console.error(`[nextgin] ${msg}`)
  process.exit(1)
}

function run(cmd: string, args: string[], env: NodeJS.ProcessEnv = {}, runCwd: string = cwd) {
  const r = spawnSync(cmd, args, { stdio: 'inherit', cwd: runCwd, env: { ...process.env, ...env } })
  if (r.error) fail(`${cmd}: ${r.error.message}`)
  if (r.status !== 0) process.exit(r.status ?? 1)
}

function hasCommand(cmd: string): boolean {
  return spawnSync(process.platform === 'win32' ? 'where' : 'which', [cmd], { stdio: 'ignore' }).status === 0
}

function finalize(outDir: string) {
  const pending = readPending(path.resolve(cwd, outDir))
  if (pending) {
    finalizeServer(pending)
    console.log(`[nextgin] finalized ${outDir}/server`)
  }
}

function resolveTarget(flags: Flags): Target {
  try {
    if (flags.target !== undefined) {
      return detectTarget({ NEXTGIN_TARGET: String(flags.target) })
    }
    return detectTarget()
  } catch (err) {
    fail(err instanceof Error ? err.message : String(err))
  }
}

function buildVercel(flags: Flags) {
  const webDir = typeof flags.web === 'string' ? flags.web : 'web'
  const backendDir = typeof flags.backend === 'string' ? flags.backend : 'backend'
  const hobby = flags['vercel-hobby'] === true
  const check = flags.check === true

  let cfg: ReturnType<typeof generateVercelConfig>
  let content: string
  try {
    const exp = runExport(path.resolve(cwd, backendDir), 'vercel')
    cfg = generateVercelConfig(exp, { webDir, backendDir, hobby })
    content = serializeVercelConfig(cfg)
  } catch (err) {
    fail(err instanceof Error ? err.message : String(err))
  }

  const outPath = path.join(cwd, 'vercel.json')
  if (check) {
    if (!fs.existsSync(outPath) || fs.readFileSync(outPath, 'utf8') !== content) {
      fail('vercel.json is out of date; run nextgin build --target vercel')
    }
    console.log('[nextgin] vercel.json is up to date')
  } else {
    fs.writeFileSync(outPath, content, 'utf8')
    const nRewrites = cfg.rewrites.length
    const nCrons = cfg.crons?.length ?? 0
    console.log(`[nextgin] wrote vercel.json (${nRewrites} rewrites, ${nCrons} crons)`)
  }

  if ((cfg.crons?.length ?? 0) > 0) {
    console.log(
      "[nextgin] note: set CRON_SECRET in the Vercel project's environment variables; nextgin rejects cron requests without it (401)",
    )
  }
}

function buildSplit(flags: Flags) {
  let opts: ReturnType<typeof validateSplitFlags>
  try {
    opts = validateSplitFlags(flags)
  } catch (err) {
    fail(err instanceof Error ? err.message : String(err))
  }

  const webDir = typeof flags.web === 'string' ? flags.web : 'web'
  const backendDir = typeof flags.backend === 'string' ? flags.backend : 'backend'
  const webAbs = path.resolve(cwd, webDir)
  const backendAbs = path.resolve(cwd, backendDir)
  const rawName = typeof flags.name === 'string' ? flags.name : path.basename(cwd)
  const force = flags.force === true
  const check = flags.check === true

  let exp: ReturnType<typeof runExport>
  let name: string
  let vercelCfg: ReturnType<typeof generateVercelSplitConfig>
  let vercelContent: string
  try {
    name = validateName(rawName)
    exp = runExport(backendAbs, 'backend')
    vercelCfg = generateVercelSplitConfig(exp, {
      backendUrl: opts.backendUrl,
      apiMode: opts.apiMode,
    })
    vercelContent = serializeVercelConfig(vercelCfg)
  } catch (err) {
    fail(err instanceof Error ? err.message : String(err))
  }

  const vercelOutPath = path.join(webAbs, 'vercel.json')
  const vercelLabel = path.join(webDir, 'vercel.json')
  if (check) {
    if (!fs.existsSync(vercelOutPath) || fs.readFileSync(vercelOutPath, 'utf8') !== vercelContent) {
      fail(`${vercelLabel} is out of date; run nextgin build --web-target vercel --backend-target ${opts.backendTarget} --backend-url ${opts.backendUrl}`)
    }
    console.log(`[nextgin] ${vercelLabel} is up to date`)
    return
  }

  const writeVercel = () => {
    fs.mkdirSync(webAbs, { recursive: true })
    fs.writeFileSync(vercelOutPath, vercelContent, 'utf8')
    const nRewrites = vercelCfg.rewrites?.length ?? 0
    console.log(`[nextgin] wrote ${vercelLabel} (${nRewrites} rewrites, mode: ${opts.apiMode})`)
    console.log(`[nextgin] note: set the Vercel project Root Directory to ${webDir}`)
    console.log(
      `[nextgin] note: set Vercel env vars NEXTGIN_INTERNAL_URL=${opts.backendUrl} and NEXTGIN_INTERNAL_SECRET (same value as on the backend)`,
    )
    if (opts.apiMode === 'direct') {
      console.log(`[nextgin] note: set NEXT_PUBLIC_NEXTGIN_API_URL=${opts.backendUrl} in Vercel`)
      console.log(
        '[nextgin] note: direct mode requires nextgin.CORS(...) on the backend and sharing cookies on the parent domain',
      )
    }
  }

  const goModPath = path.join(backendAbs, 'go.mod')
  if (!fs.existsSync(goModPath)) {
    fail(`${path.join(backendDir, 'go.mod')} not found`)
  }
  let goVersion: string
  try {
    goVersion = parseGoVersion(fs.readFileSync(goModPath, 'utf8'))
  } catch (err) {
    fail(err instanceof Error ? err.message : String(err))
  }

  const dockerfilePath = path.join(backendAbs, 'Dockerfile')
  const dockerfileLabel = path.join(backendDir, 'Dockerfile')
  const existing = fs.existsSync(dockerfilePath) ? fs.readFileSync(dockerfilePath, 'utf8') : undefined
  if (!canOverwriteDockerfile(existing) && !force) {
    fail(`${dockerfileLabel} was not generated by nextgin; pass --force to overwrite`)
  }

  writeVercel()
  fs.writeFileSync(dockerfilePath, generateDockerfile({ goVersion }), 'utf8')
  console.log(`[nextgin] wrote ${dockerfileLabel}`)
  console.log(`  docker build -t ${name}-backend ${backendDir}`)
  console.log('[nextgin] note: set NEXTGIN_INTERNAL_SECRET and CRON_SECRET on the host')
  console.log(
    '[nextgin] note: run ONE instance (the scheduler fires on every instance) or set NEXTGIN_SCHEDULER=off on replicas',
  )
  console.log(
    '[nextgin] note: private Go modules need build credentials at docker build time; a local `replace` directive must point inside the build context and be COPY-ed before `go mod download` (edit the Dockerfile and remove its first line to keep your changes)',
  )
}

function build(flags: Flags) {
  if (flags['web-target'] !== undefined || flags['backend-target'] !== undefined) {
    buildSplit(flags)
    return
  }

  const target = resolveTarget(flags)
  if (target === 'vercel') {
    buildVercel(flags)
    return
  }

  const outDir = String(flags['out-dir'] ?? '.nextgin')
  run(process.execPath, [nextBin(), 'build'], {
    NEXTGIN_ADAPTER_OPTIONS: process.env.NEXTGIN_ADAPTER_OPTIONS ?? JSON.stringify({ outDir }),
  })
  finalize(outDir)
  if (!fs.existsSync(path.join(cwd, outDir, 'manifest.json'))) {
    fail(`${outDir}/manifest.json was not produced; is adapterPath: createNextGinAdapter() set in next.config?`)
  }
  if (flags.go !== false && fs.existsSync(path.join(cwd, 'go.mod'))) {
    const out = String(flags['go-out'] ?? 'server')
    console.log(`[nextgin] go build -o ${out} .`)
    run('go', ['build', '-o', out, '.'])
  }
  console.log('[nextgin] build complete')
}

function finalizeCmd(flags: Flags) {
  const outDir = String(flags['out-dir'] ?? '.nextgin')
  if (!readPending(path.resolve(cwd, outDir))) {
    console.log(`[nextgin] nothing to finalize in ${outDir}`)
    return
  }
  finalize(outDir)
}

function dev(flags: Flags) {
  const port = String(flags.port ?? process.env.PORT ?? '8080')
  const nextPort = String(flags['next-port'] ?? '3001')
  const goURL = `http://127.0.0.1:${port}`
  const children: ChildProcess[] = []
  let shuttingDown = false

  const shutdown = (code = 0) => {
    if (shuttingDown) return
    shuttingDown = true
    for (const c of children) c.kill('SIGTERM')
    setTimeout(() => process.exit(code), 1500).unref()
  }
  process.on('SIGINT', () => shutdown(0))
  process.on('SIGTERM', () => shutdown(0))

  const nextProc = spawn(process.execPath, [nextBin(), 'dev', '-p', nextPort, '-H', '127.0.0.1'], {
    cwd,
    stdio: 'inherit',
    env: { ...process.env, NEXTGIN_INTERNAL_URL: goURL },
  })
  children.push(nextProc)
  nextProc.on('exit', (code) => shutdown(code ?? 0))

  const goEnv = { ...process.env, PORT: port, NEXTGIN_DEV_URL: `http://127.0.0.1:${nextPort}` }
  if (hasCommand('air')) {
    const air = spawn('air', [], { cwd, stdio: 'inherit', env: goEnv })
    children.push(air)
    air.on('exit', (code) => shutdown(code ?? 0))
    return
  }

  // Minimal built-in watcher: restart `go run .` when .go files change.
  let goProc: ChildProcess | null = null
  const startGo = () => {
    goProc = spawn('go', ['run', '.'], { cwd, stdio: 'inherit', env: goEnv, detached: process.platform !== 'win32' })
    children.push(goProc)
  }
  const stopGo = () =>
    new Promise<void>((resolve) => {
      const p = goProc
      if (!p || p.exitCode !== null) return resolve()
      p.once('exit', () => resolve())
      // `go run` spawns the binary as a child; signal the whole group.
      try {
        process.kill(-p.pid!, 'SIGTERM')
      } catch {
        p.kill('SIGTERM')
      }
    })
  let timer: NodeJS.Timeout | undefined
  const ignored = /(^|[/\\])(node_modules|\.next|\.nextgin|\.git)([/\\]|$)/
  fs.watch(cwd, { recursive: true }, (_evt, file) => {
    if (!file || !String(file).endsWith('.go') || ignored.test(String(file))) return
    clearTimeout(timer)
    timer = setTimeout(async () => {
      console.log(`[nextgin] ${file} changed, restarting Go server`)
      await stopGo()
      if (!shuttingDown) startGo()
    }, 200)
  })
  startGo()
}

const [cmd, ...rest] = process.argv.slice(2)
const flags = parseFlags(rest)
switch (cmd) {
  case 'build':
    build(flags)
    break
  case 'dev':
    dev(flags)
    break
  case 'finalize':
    finalizeCmd(flags)
    break
  case 'deploy':
    runDeploy(flags, { cwd }).catch((err) => fail(err instanceof Error ? err.message : String(err)))
    break
  default:
    console.log(HELP)
    process.exit(cmd && cmd !== 'help' && cmd !== '--help' ? 1 : 0)
}
