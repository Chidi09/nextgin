import type { NextginExport } from './export.js'

export interface VercelOptions {
  webDir?: string
  backendDir?: string
  hobby?: boolean
}

export interface VercelConfig {
  services: {
    web: {
      root: string
      framework: 'nextjs'
      bindings: Array<{
        type: 'service'
        service: 'backend'
        format: 'url'
        env: 'NEXTGIN_INTERNAL_URL'
      }>
    }
    backend: {
      root: string
    }
  }
  rewrites: Array<{
    source: string
    destination: { service: 'backend' | 'web' }
  }>
  crons?: Array<{
    path: string
    schedule: string
  }>
}

function normalizeServiceDir(dir: string, label: string): string {
  const cleaned = dir.replace(/^(\.\/)+/, '').replace(/\/+$/, '')
  const firstSegment = cleaned.split('/')[0]
  if (firstSegment === 'api') {
    throw new Error(
      `nextgin: ${label} "${dir}" cannot start with "api/" (Vercel treats api/ as legacy functions)`,
    )
  }
  return cleaned ? `${cleaned}/` : './'
}

/**
 * Builds a `vercel.json` configuration object from a Go backend export.
 */
export function generateVercelConfig(exp: NextginExport, opts?: VercelOptions): VercelConfig {
  const webRoot = normalizeServiceDir(opts?.webDir ?? 'web', 'webDir')
  const backendRoot = normalizeServiceDir(opts?.backendDir ?? 'backend', 'backendDir')

  const prefixes = new Set<string>()
  for (const route of exp.routes) {
    const trimmed = route.path.replace(/^\/+/, '')
    const first = trimmed.split('/')[0]
    if (!first || first.startsWith(':') || first.startsWith('*')) {
      throw new Error(
        `nextgin: route ${route.method} ${route.path} has no static first segment; Vercel needs a path prefix to route it to Go`,
      )
    }
    if (first === '_next' || first === '_nextgin') {
      throw new Error(
        `nextgin: route ${route.method} ${route.path} uses reserved prefix "${first}"`,
      )
    }
    prefixes.add(first)
  }

  if (opts?.hobby) {
    const offending = exp.crons.filter((c) => c.runsPerDayMax > 1)
    if (offending.length > 0) {
      const list = offending.map((c) => `${c.name} (${c.schedule})`).join(', ')
      throw new Error(`nextgin: Vercel Hobby only allows daily cron jobs; offending jobs: ${list}`)
    }
  }

  const sortedPrefixes = [...prefixes].sort()
  const rewrites: VercelConfig['rewrites'] = [
    ...sortedPrefixes.map((prefix) => ({
      source: `/${prefix}/:path*`,
      destination: { service: 'backend' as const },
    })),
    {
      source: '/_nextgin/:path*',
      destination: { service: 'backend' as const },
    },
    {
      source: '/(.*)',
      destination: { service: 'web' as const },
    },
  ]

  const cfg: VercelConfig = {
    services: {
      web: {
        root: webRoot,
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
        root: backendRoot,
      },
    },
    rewrites,
  }

  if (exp.crons.length > 0) {
    cfg.crons = exp.crons.map((c) => ({
      path: c.path,
      schedule: c.schedule,
    }))
  }

  return cfg
}

/**
 * Serializes a `vercel.json` config object with 2-space indentation and a trailing newline.
 */
export function serializeVercelConfig(cfg: object): string {
  return JSON.stringify(cfg, null, 2) + '\n'
}
