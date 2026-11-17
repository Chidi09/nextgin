import { randomBytes } from 'node:crypto'

export type DbMode = 'internal' | 'external' | 'none'

export interface EnvPlan {
  postgres: DbMode
  redis: DbMode
  dbName: string
  externalPostgresUrl?: string
  externalRedisUrl?: string
}

const ENV_HEADER = '# managed by nextgin; existing values are never overwritten'
const KEY_RE = /^[A-Za-z_][A-Za-z0-9_]*$/

/**
 * Parses `.env` text into a Map, keeping values verbatim (stripping only trailing `\r`).
 */
export function parseEnv(text: string): Map<string, string> {
  const map = new Map<string, string>()

  for (const rawLine of text.split('\n')) {
    const line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine
    if (line.trim() === '' || line.startsWith('#')) {
      continue
    }

    const eq = line.indexOf('=')
    if (eq === -1) {
      continue
    }

    const key = line.slice(0, eq)
    if (!KEY_RE.test(key)) {
      continue
    }

    const value = line.slice(eq + 1)
    map.set(key, value)
  }

  return map
}

/**
 * Serializes a `.env` Map with the managed header and insertion-ordered `KEY=VALUE` lines.
 */
export function serializeEnv(env: Map<string, string>): string {
  const lines = [ENV_HEADER]
  for (const [key, value] of env.entries()) {
    lines.push(`${key}=${value}`)
  }
  return `${lines.join('\n')}\n`
}

/**
 * Generates a 48-character URL-safe hex secret.
 */
export function newSecret(): string {
  return randomBytes(24).toString('hex')
}

/**
 * Merges database/redis configuration into a copy of `existing` without overwriting existing keys.
 */
export function mergeEnv(
  existing: Map<string, string>,
  plan: EnvPlan,
  gen: () => string = newSecret,
): Map<string, string> {
  const out = new Map(existing)

  if (!out.has('NEXTGIN_POSTGRES')) {
    out.set('NEXTGIN_POSTGRES', plan.postgres)
  }
  if (!out.has('NEXTGIN_REDIS')) {
    out.set('NEXTGIN_REDIS', plan.redis)
  }

  if (plan.postgres === 'internal') {
    if (!out.has('POSTGRES_PASSWORD')) {
      out.set('POSTGRES_PASSWORD', gen())
    }
    if (!out.has('DATABASE_URL')) {
      const pw = out.get('POSTGRES_PASSWORD')!
      out.set('DATABASE_URL', `postgres://nextgin:${pw}@postgres:5432/${plan.dbName}?sslmode=disable`)
    }
  } else if (plan.postgres === 'external') {
    if (!out.has('DATABASE_URL')) {
      if (!plan.externalPostgresUrl) {
        throw new Error('nextgin deploy: external Postgres URL is required')
      }
      // `.env` is consumed only by Docker Compose, which interpolates `$`; double `$` to `$$` on disk.
      out.set('DATABASE_URL', plan.externalPostgresUrl.split('$').join('$$'))
    }
  }

  if (plan.redis === 'internal') {
    if (!out.has('REDIS_PASSWORD')) {
      out.set('REDIS_PASSWORD', gen())
    }
    if (!out.has('REDIS_URL')) {
      const pw = out.get('REDIS_PASSWORD')!
      out.set('REDIS_URL', `redis://:${pw}@redis:6379/0`)
    }
  } else if (plan.redis === 'external') {
    if (!out.has('REDIS_URL')) {
      if (!plan.externalRedisUrl) {
        throw new Error('nextgin deploy: external Redis URL is required')
      }
      // `.env` is consumed only by Docker Compose, which interpolates `$`; double `$` to `$$` on disk.
      out.set('REDIS_URL', plan.externalRedisUrl.split('$').join('$$'))
    }
  }

  return out
}

/**
 * Returns a copy of `env` with the service mode and connection URL removed while keeping passwords.
 */
export function resetService(env: Map<string, string>, which: 'postgres' | 'redis'): Map<string, string> {
  const out = new Map(env)
  if (which === 'postgres') {
    out.delete('NEXTGIN_POSTGRES')
    out.delete('DATABASE_URL')
  } else {
    out.delete('NEXTGIN_REDIS')
    out.delete('REDIS_URL')
  }
  return out
}
