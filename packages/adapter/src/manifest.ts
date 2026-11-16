import path from 'node:path'

/**
 * Schema of `.nextgin/manifest.json`, consumed by the Go runtime
 * (see manifest.go). Bump MANIFEST_VERSION on breaking changes.
 */
export const MANIFEST_VERSION = 1

export type HeaderMap = Record<string, string | string[]>

export interface StaticFileEntry {
  /** Decoded URL pathname, e.g. `/_next/static/chunks/abc.js` or `/robots.txt`. */
  pathname: string
  /** Path relative to the .nextgin dir, always forward slashes. */
  file: string
  immutable: boolean
  /** Response headers baked in at build time (cache-control etc.). */
  headers: HeaderMap
}

export interface PrerenderEntry {
  pathname: string
  file: string
  status: number
  headers: HeaderMap
}

export type BypassKind = 'headers' | 'redirect' | 'rewrite' | 'middleware'

/**
 * A routing rule the Go fast path must respect. If a rule's regex matches a
 * request Go would otherwise serve from disk, Go either applies the rule's
 * headers (`kind === 'headers' && applicable`) or hands the request to the
 * worker so Next.js evaluates it.
 */
export interface BypassRule {
  kind: BypassKind
  source?: string
  sourceRegex: string
  /** Only for `headers`: static header values safe for Go to apply itself. */
  headers?: Record<string, string>
  applicable: boolean
}

export interface SkippedPrerender {
  pathname: string
  reason: string
}

export interface NextGinManifest {
  version: number
  buildId: string
  nextVersion: string
  basePath: string
  trailingSlash: boolean
  worker: {
    /** Directory (relative to .nextgin) the worker runs in. */
    dir: string
    /** Worker entry file (relative to .nextgin). */
    entry: string
  }
  staticFiles: StaticFileEntry[]
  prerendered: PrerenderEntry[]
  bypass: BypassRule[]
  /** Raw Next.js routing, kept for diagnostics/tooling. Go does not evaluate it. */
  routing: {
    dynamicRoutes: RouteLike[]
    beforeMiddleware: RouteLike[]
    beforeFiles: RouteLike[]
    afterFiles: RouteLike[]
    fallback: RouteLike[]
  }
  skippedPrerenders: SkippedPrerender[]
}

// ---- Minimal structural views of the Next.js adapter context -------------
// We avoid importing Next's internal types so the adapter keeps working
// across minor versions that reshuffle them.

export interface RouteHasLike {
  type: string
  key?: string
  value?: string
}

export interface RouteLike {
  source?: string
  sourceRegex: string
  destination?: string
  headers?: Record<string, string>
  has?: RouteHasLike[]
  missing?: RouteHasLike[]
  status?: number
  priority?: boolean
}

export interface StaticFileOutput {
  pathname: string
  filePath: string
  immutableHash?: string
}

export interface PrerenderOutput {
  pathname: string
  routeType?: 'route' | 'fallback' | 'shell' | 'page'
  response?: 'empty' | 'initial' | 'complete'
  compute?: 'blocking' | 'resuming' | 'static'
  pprChain?: unknown
  fallback?: {
    filePath?: string
    initialStatus?: number
    initialHeaders?: HeaderMap
    initialRevalidate?: number | false
    postponedState?: string
  }
}

export interface BuildContextLike {
  buildId: string
  nextVersion: string
  routing: {
    beforeMiddleware: RouteLike[]
    middlewareMatchers?: RouteLike[]
    beforeFiles: RouteLike[]
    afterFiles: RouteLike[]
    dynamicRoutes: RouteLike[]
    onMatch: RouteLike[]
    fallback: RouteLike[]
  }
  outputs: {
    staticFiles: StaticFileOutput[]
    prerenders: PrerenderOutput[]
    middleware?: { config?: { matchers?: RouteLike[] } }
  }
  config: {
    basePath?: string
    trailingSlash?: boolean
    expireTime?: number
  }
}

export interface CopyOp {
  from: string
  /** Relative to the .nextgin dir. */
  to: string
}

export interface ManifestPlan {
  manifest: NextGinManifest
  copies: CopyOp[]
}

// Headers Next.js uses internally that should not leak to clients.
const INTERNAL_HEADERS = new Set(['x-next-cache-tags'])

// Pathnames that have their own status semantics in Next.js and must go
// through the worker (e.g. `/404` must answer with a 404 status, not 200).
const ERROR_PAGES = new Set(['/404', '/500'])

function safeDecode(p: string): string {
  try {
    return decodeURIComponent(p)
  } catch {
    return p
  }
}

function toPosix(p: string): string {
  return p.split(path.sep).join('/')
}

function compile(regex: string): RegExp | null {
  try {
    return new RegExp(regex)
  } catch {
    return null
  }
}

function lowerKeys(h: Record<string, string>): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(h)) out[k.toLowerCase()] = v
  return out
}

/** Builds the bypass rule list Go uses to decide when *not* to serve from disk. */
export function buildBypassRules(ctx: BuildContextLike): BypassRule[] {
  const rules: BypassRule[] = []

  for (const r of [...ctx.routing.beforeMiddleware, ...ctx.routing.beforeFiles]) {
    if (r.status && r.headers?.Location !== undefined) {
      rules.push({ kind: 'redirect', source: r.source, sourceRegex: r.sourceRegex, applicable: false })
    } else if (r.destination !== undefined) {
      rules.push({ kind: 'rewrite', source: r.source, sourceRegex: r.sourceRegex, applicable: false })
    } else if (r.headers) {
      const hasConditions = (r.has?.length ?? 0) > 0 || (r.missing?.length ?? 0) > 0
      // Values referencing captured params ($1, $slug) need Next's matcher.
      const dynamic = Object.values(r.headers).some((v) => v.includes('$'))
      rules.push({
        kind: 'headers',
        source: r.source,
        sourceRegex: r.sourceRegex,
        headers: lowerKeys(r.headers),
        applicable: !hasConditions && !dynamic && !r.status,
      })
    } else {
      // Unknown shape: be conservative.
      rules.push({ kind: 'rewrite', source: r.source, sourceRegex: r.sourceRegex, applicable: false })
    }
  }

  const matchers = ctx.routing.middlewareMatchers ?? ctx.outputs.middleware?.config?.matchers ?? []
  for (const m of matchers) {
    rules.push({ kind: 'middleware', source: m.source, sourceRegex: m.sourceRegex, applicable: false })
  }
  return rules
}

function prerenderFileName(pathname: string, sourceFile: string): string {
  const base = pathname === '/' ? '/index' : pathname
  const ext = path.extname(sourceFile)
  return `prerendered${base}${ext && !base.endsWith(ext) ? ext : ''}`
}

function staticFileName(pathname: string, sourceFile: string): string {
  const ext = path.extname(sourceFile)
  const needsExt = ext && path.posix.extname(pathname) === ''
  return `static${pathname}${needsExt ? ext : ''}`
}

/** Decides whether Go may serve a prerender straight from disk. */
export function prerenderEligibility(p: PrerenderOutput): string | null {
  if (!p.routeType) return 'not a primary output (rsc/segment sibling)'
  if (p.pathname.startsWith('/_')) return 'internal route'
  if (p.pathname.includes('[')) return 'unfilled dynamic params'
  if (!p.fallback?.filePath) return 'no fallback file'
  if (p.compute !== 'static') return `compute=${p.compute}`
  if (p.response !== 'complete') return `response=${p.response}`
  if (p.fallback.initialRevalidate !== false) return `revalidate=${String(p.fallback.initialRevalidate)}`
  if (p.fallback.postponedState) return 'postponed (PPR)'
  if (p.pprChain) return 'ppr chain'
  const status = p.fallback.initialStatus ?? 200
  if (status !== 200) return `status=${status}`
  return null
}

export interface PublicFile {
  /** Path relative to public/, forward slashes. */
  relPath: string
  filePath: string
}

export interface BuildManifestOptions {
  /** Worker dir relative to .nextgin, e.g. `server` or `server/apps/web`. */
  workerDir: string
  /** Files under public/ (Next.js does not list them in adapter outputs). */
  publicFiles?: PublicFile[]
}

export function buildManifest(ctx: BuildContextLike, opts: BuildManifestOptions): ManifestPlan {
  const copies: CopyOp[] = []
  const onMatch = ctx.routing.onMatch
    .filter((r) => r.headers && !r.has && !r.missing)
    .map((r) => ({ re: compile(r.sourceRegex), headers: lowerKeys(r.headers!) }))
    .filter((r): r is { re: RegExp; headers: Record<string, string> } => r.re !== null)

  const staticFiles: StaticFileEntry[] = []
  const seenStatic = new Set<string>()
  for (const f of ctx.outputs.staticFiles) {
    const pathname = safeDecode(f.pathname)
    if (ERROR_PAGES.has(pathname) || seenStatic.has(pathname)) continue
    seenStatic.add(pathname)

    const headers: HeaderMap = {}
    for (const rule of onMatch) {
      if (rule.re.test(f.pathname)) Object.assign(headers, rule.headers)
    }
    const immutable = Boolean(f.immutableHash) || /immutable/.test(String(headers['cache-control'] ?? ''))
    if (!headers['cache-control']) {
      headers['cache-control'] = immutable ? 'public, max-age=31536000, immutable' : 'public, max-age=0'
    }
    const file = staticFileName(pathname, f.filePath)
    staticFiles.push({ pathname, file, immutable, headers })
    copies.push({ from: f.filePath, to: file })
  }

  // Build outputs win over public/ on conflicts, matching Next.js.
  for (const f of opts.publicFiles ?? []) {
    const pathname = `${ctx.config.basePath ?? ''}/${f.relPath}`
    if (seenStatic.has(pathname)) continue
    seenStatic.add(pathname)
    const file = `static${pathname}`
    staticFiles.push({ pathname, file, immutable: false, headers: { 'cache-control': 'public, max-age=0' } })
    copies.push({ from: f.filePath, to: file })
  }

  const expire = ctx.config.expireTime ?? 31536000
  const prerendered: PrerenderEntry[] = []
  const skippedPrerenders: SkippedPrerender[] = []
  for (const p of ctx.outputs.prerenders) {
    const reason = prerenderEligibility(p)
    if (reason) {
      // Siblings are noise; only report primary outputs.
      if (p.routeType) skippedPrerenders.push({ pathname: p.pathname, reason })
      continue
    }
    const fb = p.fallback!
    const headers: HeaderMap = {}
    for (const [k, v] of Object.entries(fb.initialHeaders ?? {})) {
      if (!INTERNAL_HEADERS.has(k.toLowerCase())) headers[k.toLowerCase()] = v
    }
    // Mirrors what `next start` sends for revalidate=false prerenders.
    headers['cache-control'] = `s-maxage=${expire}`
    headers['x-nextjs-cache'] = 'HIT'
    const pathname = safeDecode(p.pathname)
    const file = prerenderFileName(pathname, fb.filePath!)
    prerendered.push({ pathname, file, status: fb.initialStatus ?? 200, headers })
    copies.push({ from: fb.filePath!, to: file })
  }

  const workerDir = toPosix(opts.workerDir)
  const manifest: NextGinManifest = {
    version: MANIFEST_VERSION,
    buildId: ctx.buildId,
    nextVersion: ctx.nextVersion,
    basePath: ctx.config.basePath ?? '',
    trailingSlash: Boolean(ctx.config.trailingSlash),
    worker: { dir: workerDir, entry: `${workerDir}/nextgin-worker.cjs` },
    staticFiles,
    prerendered,
    bypass: buildBypassRules(ctx),
    routing: {
      dynamicRoutes: ctx.routing.dynamicRoutes,
      beforeMiddleware: ctx.routing.beforeMiddleware,
      beforeFiles: ctx.routing.beforeFiles,
      afterFiles: ctx.routing.afterFiles,
      fallback: ctx.routing.fallback,
    },
    skippedPrerenders,
  }
  return { manifest, copies }
}
