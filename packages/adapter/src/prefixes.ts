import type { NextginExport } from './export.js'

/**
 * Extracts, validates, deduplicates, and sorts the static first path segments
 * of all exported Go routes.
 */
export function goPrefixes(exp: NextginExport): string[] {
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
  return [...prefixes].sort()
}
