import fs from 'node:fs'
import path from 'node:path'
import { buildManifest, type BuildContextLike, type PublicFile } from './manifest.js'
import { readOptions } from './options.js'
import { finalizeServer, readPending, PENDING_FILE, type PendingServer } from './finalize.js'

const BUILD_PHASE = 'phase-production-build'

// Structural subset of Next's NextAdapter so we do not depend on internal type paths.
interface AdapterContext extends BuildContextLike {
  projectDir: string
  repoRoot: string
  distDir: string
  config: BuildContextLike['config'] & { outputFileTracingRoot?: string; output?: string }
}

function log(msg: string) {
  console.log(`[nextgin] ${msg}`)
}

function listPublicFiles(projectDir: string): PublicFile[] {
  const root = path.join(projectDir, 'public')
  if (!fs.existsSync(root)) return []
  const out: PublicFile[] = []
  for (const ent of fs.readdirSync(root, { recursive: true, withFileTypes: true })) {
    if (!ent.isFile()) continue
    const filePath = path.join(ent.parentPath, ent.name)
    out.push({ relPath: path.relative(root, filePath).split(path.sep).join('/'), filePath })
  }
  return out
}

const adapter = {
  name: 'nextgin',

  modifyConfig(config: Record<string, any>, ctx: { phase: string }) {
    // modifyConfig runs for every CLI command; only force standalone for builds.
    if (ctx.phase !== BUILD_PHASE) return config
    if (config.output === 'export') {
      throw new Error('[nextgin] `output: "export"` is not supported; nextgin needs the standalone server.')
    }
    return { ...config, output: 'standalone' }
  },

  async onBuildComplete(ctx: AdapterContext) {
    const opts = readOptions()
    const outDir = path.resolve(ctx.projectDir, opts.outDir)
    fs.rmSync(outDir, { recursive: true, force: true })
    fs.mkdirSync(outDir, { recursive: true })

    const tracingRoot = ctx.config.outputFileTracingRoot ?? ctx.repoRoot
    const appRel = path.relative(tracingRoot, ctx.projectDir)
    const workerDir = path.join('server', appRel)

    const { manifest, copies } = buildManifest(ctx, { workerDir, publicFiles: listPublicFiles(ctx.projectDir) })

    for (const c of copies) {
      const dest = path.join(outDir, c.to)
      fs.mkdirSync(path.dirname(dest), { recursive: true })
      fs.copyFileSync(c.from, dest)
    }

    if (opts.includeServer) {
      const pending: PendingServer = {
        projectDir: ctx.projectDir,
        distDir: ctx.distDir,
        outDir,
        workerDir: manifest.worker.dir,
        workerEntry: manifest.worker.entry,
      }
      fs.writeFileSync(path.join(outDir, PENDING_FILE), JSON.stringify(pending, null, 2))
      // Next.js writes .next/standalone after onBuildComplete returns, so copy
      // it once the build process exits successfully. `nextgin build` also
      // runs finalize explicitly (idempotent).
      process.once('exit', (code) => {
        if (code !== 0 || !readPending(outDir)) return
        try {
          finalizeServer(pending)
          log(`copied standalone server to ${path.relative(ctx.projectDir, outDir)}/server`)
        } catch (err) {
          console.error(String(err))
          console.error('[nextgin] run `npx nextgin finalize` after the build completes.')
        }
      })
    }

    fs.writeFileSync(path.join(outDir, 'manifest.json'), JSON.stringify(manifest, null, 2))
    log(
      `wrote ${path.relative(ctx.projectDir, outDir)}/manifest.json: ` +
        `${manifest.staticFiles.length} static files, ${manifest.prerendered.length} prerendered routes served by Go, ` +
        `${manifest.skippedPrerenders.length} routed to the worker, ${manifest.bypass.length} bypass rules`,
    )
  },
}

export default adapter
