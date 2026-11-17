import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
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
  // The export is written to a file (NEXTGIN_EXPORT_FILE) rather than read from
  // stdout, so gin debug output or user logging cannot corrupt it.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'nextgin-export-'))
  const file = path.join(dir, 'export.json')
  try {
    const result = spawnSync('go', ['run', '.'], {
      cwd: backendDir,
      env: { ...process.env, NEXTGIN_EXPORT: '1', NEXTGIN_EXPORT_FILE: file, NEXTGIN_TARGET: target },
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

    if (!fs.existsSync(file)) {
      throw new Error(
        'nextgin: go export produced no output; does main() call app.Run() after registering routes and cron jobs?',
      )
    }
    return parseExport(fs.readFileSync(file, 'utf8'))
  } finally {
    fs.rmSync(dir, { recursive: true, force: true })
  }
}
