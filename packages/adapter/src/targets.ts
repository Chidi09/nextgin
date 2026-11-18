export type Target = 'selfhost' | 'vercel' | 'backend'

const VALID_TARGETS = new Set<string>(['selfhost', 'vercel', 'backend'])

/**
 * Detects the active deployment target from environment variables.
 *
 * Precedence:
 * 1. `NEXTGIN_TARGET` if non-empty (must be `'selfhost' | 'vercel' | 'backend'`).
 * 2. `'vercel'` if `VERCEL === '1'`.
 * 3. `'selfhost'` otherwise.
 */
export function detectTarget(env: NodeJS.ProcessEnv = process.env): Target {
  const explicit = env.NEXTGIN_TARGET
  if (explicit) {
    if (!VALID_TARGETS.has(explicit)) {
      throw new Error(`nextgin: unknown NEXTGIN_TARGET "${explicit}"`)
    }
    return explicit as Target
  }
  if (env.VERCEL === '1') {
    return 'vercel'
  }
  return 'selfhost'
}
