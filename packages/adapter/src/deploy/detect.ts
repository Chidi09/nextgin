import { existsSync } from 'node:fs'
import { join } from 'node:path'

export interface Layout {
  kind: 'split' | 'root'
  webDir: string
  backendDir: string
}

/**
 * Detects whether the project uses a split (`web/` + `backend/`) or root layout.
 */
export function detectLayout(root: string): Layout {
  if (existsSync(join(root, 'web', 'package.json')) && existsSync(join(root, 'backend', 'go.mod'))) {
    return { kind: 'split', webDir: 'web', backendDir: 'backend' }
  }

  if (existsSync(join(root, 'package.json')) && existsSync(join(root, 'go.mod'))) {
    return { kind: 'root', webDir: '.', backendDir: '.' }
  }

  throw new Error(
    'nextgin deploy: no project found; expected web/package.json + backend/go.mod, or package.json + go.mod in the project root',
  )
}

const POSTGRES_MODULES = [
  /^github\.com\/jackc\/pgx(\/v\d+)?$/,
  /^github\.com\/lib\/pq$/,
  /^gorm\.io\/driver\/postgres$/,
]

const REDIS_MODULES = [
  /^github\.com\/redis\/go-redis(\/v\d+)?$/,
  /^github\.com\/gomodule\/redigo$/,
]

/**
 * Detects Postgres and Redis dependencies from `go.mod` direct `require` entries.
 */
export function detectServices(goMod: string): { postgres: boolean; redis: boolean } {
  const modules: string[] = []
  let inRequireBlock = false

  for (const rawLine of goMod.split(/\r?\n/)) {
    if (/\/\/\s*indirect\b/.test(rawLine)) {
      continue
    }

    const line = rawLine.replace(/\/\/.*$/, '').trim()
    if (line === '') {
      continue
    }

    if (inRequireBlock) {
      if (line === ')') {
        inRequireBlock = false
        continue
      }
      const mod = line.split(/\s+/)[0]
      if (mod) {
        modules.push(mod)
      }
      continue
    }

    if (/^require\s*\($/.test(line)) {
      inRequireBlock = true
      continue
    }

    const single = /^require\s+(\S+)/.exec(line)
    if (single && single[1] !== '(') {
      modules.push(single[1])
    }
  }

  const postgres = modules.some((m) => POSTGRES_MODULES.some((re) => re.test(m)))
  const redis = modules.some((m) => REDIS_MODULES.some((re) => re.test(m)))

  return { postgres, redis }
}
