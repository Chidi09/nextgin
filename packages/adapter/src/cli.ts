#!/usr/bin/env node
import { spawn, spawnSync, type ChildProcess } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import { createRequire } from 'node:module'
import { finalizeServer, readPending } from './finalize.js'
import { runExport } from './export.js'
import { detectTarget, type Target } from './targets.js'
import { generateVercelConfig, serializeVercelConfig } from './vercel.js'

const HELP = `nextgin — Next.js on a Go/Gin front door

Usage:
  nextgin build [--target selfhost|vercel|cloudflare] [--out-dir .nextgin] [--go-out server] [--no-go] [--web web] [--backend backend] [--check] [--vercel-hobby]
      selfhost (default): runs \`next build\` (with @nextgin/adapter), finalizes
      .nextgin/server and compiles the Go binary from the current directory.
      vercel: exports Go routes/crons and writes vercel.json (or verifies it
      with --check; --vercel-hobby rejects sub-daily crons).

  nextgin dev [--port 8080] [--next-port 3001]
      Runs \`next dev\` and the Go server side by side. Go proxies every
      non-API request (including HMR) to next dev and restarts on .go changes
      (uses \`air\` when installed).

  nextgin finalize [--out-dir .nextgin]
      Copies .next/standalone into .nextgin/server (normally automatic).
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

function run(cmd: string, args: string[], env: NodeJS.ProcessEnv = {}) {
  const r = spawnSync(cmd, args, { stdio: 'inherit', cwd, env: { ...process.env, ...env } })
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
    return
  }

  fs.writeFileSync(outPath, content, 'utf8')
  const nRewrites = cfg.rewrites.length
  const nCrons = cfg.crons?.length ?? 0
  console.log(`[nextgin] wrote vercel.json (${nRewrites} rewrites, ${nCrons} crons)`)
}

function build(flags: Flags) {
  const target = resolveTarget(flags)
  if (target === 'cloudflare') {
    fail('the cloudflare target is not implemented yet')
  }
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
  default:
    console.log(HELP)
    process.exit(cmd && cmd !== 'help' && cmd !== '--help' ? 1 : 0)
}
