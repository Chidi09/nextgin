import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const WORKER_TEMPLATE = fileURLToPath(new URL('../templates/worker.cjs', import.meta.url))

/** Written by onBuildComplete so finalize can run later (exit hook or CLI). */
export interface PendingServer {
  projectDir: string
  distDir: string
  /** Absolute path of the .nextgin dir. */
  outDir: string
  /** Worker dir relative to outDir. */
  workerDir: string
  /** Worker entry relative to outDir. */
  workerEntry: string
}

export const PENDING_FILE = 'pending-server.json'

/**
 * Copies `.next/standalone` (+ static assets and public/) into
 * `.nextgin/server` and drops the worker entry next to it.
 *
 * Next.js writes the standalone output *after* adapters' onBuildComplete
 * runs, which is why this is a separate step. Synchronous on purpose: it is
 * also called from a process 'exit' handler.
 */
export function finalizeServer(p: PendingServer): void {
  const standalone = path.join(p.distDir, 'standalone')
  const appRel = path.relative(path.join(p.outDir, "server"), path.join(p.outDir, p.workerDir))
  if (!fs.existsSync(path.join(standalone, appRel, "server.js"))) {
    throw new Error(`[nextgin] ${standalone} is missing or incomplete; standalone output is required for the worker.`)
  }
  const serverRoot = path.join(p.outDir, 'server')
  fs.rmSync(serverRoot, { recursive: true, force: true })
  // verbatimSymlinks keeps pnpm-style relative links valid inside the copy.
  fs.cpSync(standalone, serverRoot, { recursive: true, verbatimSymlinks: true })

  // The worker also needs static assets + public/ for requests Go forwards
  // (e.g. when middleware matches an asset path).
  const appServerDir = path.join(p.outDir, p.workerDir)
  const distRel = path.relative(p.projectDir, p.distDir)
  fs.cpSync(path.join(p.distDir, 'static'), path.join(appServerDir, distRel, 'static'), { recursive: true })
  const publicDir = path.join(p.projectDir, 'public')
  if (fs.existsSync(publicDir)) {
    fs.cpSync(publicDir, path.join(appServerDir, 'public'), { recursive: true })
  }
  fs.copyFileSync(WORKER_TEMPLATE, path.join(p.outDir, p.workerEntry))
  fs.rmSync(path.join(p.outDir, PENDING_FILE), { force: true })
}

export function readPending(outDir: string): PendingServer | null {
  const file = path.join(outDir, PENDING_FILE)
  if (!fs.existsSync(file)) return null
  return JSON.parse(fs.readFileSync(file, 'utf8'))
}
