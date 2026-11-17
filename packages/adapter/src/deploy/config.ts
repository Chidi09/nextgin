export const DEPLOY_CONFIG_FILE = 'nextgin.deploy.json'

export interface DeployConfig {
  host?: string
  name?: string
  domain?: string
  sshPort?: number
}

const ALLOWED_KEYS = new Set(['host', 'name', 'domain', 'sshPort'])

/**
 * Validates an SSH host (`host` or `user@host`), rejecting flags, whitespace, and shell metacharacters.
 */
export function validateHost(host: string): string {
  if (typeof host !== 'string' || host.length === 0 || host.startsWith('-')) {
    throw new Error(`nextgin deploy: invalid --host "${String(host)}"`)
  }

  let hostname = host
  const atIndex = host.indexOf('@')
  if (atIndex !== -1) {
    if (host.indexOf('@', atIndex + 1) !== -1) {
      throw new Error(`nextgin deploy: invalid --host "${host}"`)
    }
    const user = host.slice(0, atIndex)
    hostname = host.slice(atIndex + 1)
    if (!/^[A-Za-z_][A-Za-z0-9._-]{0,31}$/.test(user)) {
      throw new Error(`nextgin deploy: invalid --host "${host}"`)
    }
  }

  const isHostnameOrIpv4 = /^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$/.test(hostname)
  const isBracketFreeIpv6 = hostname.includes(':') && /^[0-9A-Fa-f:]+$/.test(hostname)
  if (!isHostnameOrIpv4 && !isBracketFreeIpv6) {
    throw new Error(`nextgin deploy: invalid --host "${host}"`)
  }

  return host
}

/**
 * Normalizes a project name to lowercase `[a-z0-9-]` up to 40 chars.
 */
export function validateName(raw: string): string {
  if (typeof raw !== 'string') {
    throw new Error(`nextgin deploy: invalid --name "${String(raw)}"`)
  }

  const normalized = raw
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 40)
    .replace(/-+$/g, '')

  if (normalized.length === 0) {
    throw new Error(`nextgin deploy: invalid --name "${raw}"`)
  }

  return normalized
}

/**
 * Validates and lowercases a domain name (no scheme, port, path, or wildcard).
 */
export function validateDomain(domain: string): string {
  if (typeof domain !== 'string') {
    throw new Error(`nextgin deploy: invalid --domain "${String(domain)}"`)
  }

  const lower = domain.toLowerCase()
  if (!/^(?!-)([a-z0-9-]{1,63}\.)+[a-z]{2,63}$/.test(lower)) {
    throw new Error(`nextgin deploy: invalid --domain "${domain}"`)
  }

  return lower
}

/**
 * Validates an SSH port in the range 1..65535 (accepts integer numbers or numeric strings).
 */
export function validateSshPort(p: unknown): number {
  let port: number
  if (typeof p === 'number') {
    port = p
  } else if (typeof p === 'string' && /^\d+$/.test(p)) {
    port = Number(p)
  } else {
    throw new Error('nextgin deploy: invalid --ssh-port')
  }

  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error('nextgin deploy: invalid --ssh-port')
  }

  return port
}

/**
 * Parses and validates `nextgin.deploy.json`, rejecting unknown or secret-bearing keys.
 */
export function parseDeployConfig(text: string): DeployConfig {
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err)
    throw new Error(`nextgin deploy: invalid ${DEPLOY_CONFIG_FILE}: ${msg}`)
  }

  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error(`nextgin deploy: ${DEPLOY_CONFIG_FILE} must be a JSON object`)
  }

  const obj = parsed as Record<string, unknown>
  for (const key of Object.keys(obj)) {
    if (!ALLOWED_KEYS.has(key)) {
      throw new Error(`nextgin deploy: unknown key "${key}" in ${DEPLOY_CONFIG_FILE}`)
    }
  }

  const result: DeployConfig = {}
  if (obj.host !== undefined) {
    result.host = validateHost(obj.host as string)
  }
  if (obj.name !== undefined) {
    result.name = validateName(obj.name as string)
  }
  if (obj.domain !== undefined) {
    result.domain = validateDomain(obj.domain as string)
  }
  if (obj.sshPort !== undefined) {
    result.sshPort = validateSshPort(obj.sshPort)
  }

  return result
}

/**
 * Serializes a DeployConfig with fixed key order (`host`, `name`, `domain`, `sshPort`), omitting undefined keys.
 */
export function serializeDeployConfig(cfg: DeployConfig): string {
  const out: DeployConfig = {}
  if (cfg.host !== undefined) out.host = cfg.host
  if (cfg.name !== undefined) out.name = cfg.name
  if (cfg.domain !== undefined) out.domain = cfg.domain
  if (cfg.sshPort !== undefined) out.sshPort = cfg.sshPort
  return `${JSON.stringify(out, null, 2)}\n`
}
