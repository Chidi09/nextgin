import { spawnSync } from 'node:child_process'
import type { Target } from './targets.js'

export interface ExportRoute {
  method: string
  path: string
}

export interface ExportCron {
  name: string
  schedule: string
  cloudflare: string
  path: string
  runsPerDayMax: number
}

export interface NextginExport {
  version: 1
  routes: ExportRoute[]
  crons: ExportCron[]
}

const MAX_EXPORT_BUFFER = 16 * 1024 * 1024

/**
 * Parses and validates the JSON output produced by `NEXTGIN_EXPORT=1 go run .`.
 */
export function parseExport(json: string): NextginExport {
  let parsed: unknown
  try {
    parsed = JSON.parse(json)
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err)
    throw new Error(`nextgin: invalid export JSON: ${msg}`)
  }

  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('nextgin: invalid export: expected a JSON object')
  }

  const obj = parsed as Record<string, unknown>
  if (obj.version !== 1) {
    throw new Error(`nextgin: invalid export: unsupported version ${String(obj.version)}`)
  }
  if (!Array.isArray(obj.routes)) {
    throw new Error('nextgin: invalid export: "routes" must be an array')
  }
  if (!Array.isArray(obj.crons)) {
    throw new Error('nextgin: invalid export: "crons" must be an array')
  }

  return parsed as NextginExport
}

/**
 * Executes the Go backend in export mode (`NEXTGIN_EXPORT=1`) and parses its JSON output.
 */
export function runExport(backendDir: string, target: Target): NextginExport {
  const result = spawnSync('go', ['run', '.'], {
    cwd: backendDir,
    env: { ...process.env, NEXTGIN_EXPORT: '1', NEXTGIN_TARGET: target },
    encoding: 'utf8',
    maxBuffer: MAX_EXPORT_BUFFER,
  })

  if (result.error) {
    const stderr = (result.stderr ?? '').trim()
    throw new Error(
      `nextgin: failed to run go export: ${result.error.message}${stderr ? `\n${stderr}` : ''}`,
    )
  }

  if (result.status !== 0) {
    const stderr = (result.stderr ?? '').trim()
    throw new Error(
      `nextgin: go export exited with status ${result.status}${stderr ? `: ${stderr}` : ''}`,
    )
  }

  return parseExport(result.stdout)
}
