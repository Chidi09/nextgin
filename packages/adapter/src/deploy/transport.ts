import { spawn, spawnSync } from 'node:child_process'
import { chmodSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname } from 'node:path'
import { createGzip } from 'node:zlib'

export interface ExecResult {
  status: number
  stdout: string
  stderr: string
}

export interface Transport {
  readonly kind: 'ssh' | 'local'
  readonly label: string
  exec(script: string): ExecResult
  put(remotePath: string, data: string | Buffer, mode: string): void
  readFile(remotePath: string): string | undefined
  loadImage(ref: string, dockerCmd?: string): Promise<void> | void
}

export interface SshTransportOptions {
  host: string
  port?: number
  identity?: string
}

const MODE_RE = /^[0-7]{3,4}$/

/**
 * POSIX single-quote quoting for safe interpolation into `/bin/sh` scripts.
 */
export function shQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}

/**
 * Builds the `ssh` argv array for running a `/bin/sh` script on the remote host.
 */
export function buildSshArgs(o: SshTransportOptions, script: string): string[] {
  if (!o.host || o.host.startsWith('-')) {
    throw new Error(`nextgin deploy: invalid ssh host "${String(o.host)}"`)
  }
  if (o.identity !== undefined && (o.identity.length === 0 || o.identity.startsWith('-'))) {
    throw new Error(`nextgin deploy: invalid ssh identity "${String(o.identity)}"`)
  }

  const port = o.port ?? 22
  const args = [
    '-o',
    'BatchMode=yes',
    '-o',
    'ConnectTimeout=10',
    '-p',
    String(port),
  ]
  if (o.identity !== undefined) {
    args.push('-i', o.identity)
  }
  args.push(o.host, '--', 'sh', '-c', shQuote(script))
  return args
}

function validateMode(mode: string): void {
  if (!MODE_RE.test(mode)) {
    throw new Error(`nextgin deploy: invalid file mode "${mode}"`)
  }
}

/**
 * Creates an SSH-backed Transport using the system `ssh` binary with argv arrays.
 */
export function createSshTransport(o: SshTransportOptions): Transport {
  // Validate upfront so invalid host/identity fails immediately.
  buildSshArgs(o, 'true')

  const exec = (script: string, input?: string | Buffer): ExecResult => {
    const args = buildSshArgs(o, script)
    const r = spawnSync('ssh', args, {
      input,
      encoding: 'utf8',
      maxBuffer: 16 * 1024 * 1024,
    })
    if (r.error) {
      return {
        status: r.status ?? 1,
        stdout: r.stdout ?? '',
        stderr: r.stderr ? `${r.stderr}\n${r.error.message}` : r.error.message,
      }
    }
    return {
      status: r.status ?? 1,
      stdout: r.stdout ?? '',
      stderr: r.stderr ?? '',
    }
  }

  return {
    kind: 'ssh',
    label: o.host,

    exec(script: string): ExecResult {
      return exec(script)
    },

    put(remotePath: string, data: string | Buffer, mode: string): void {
      validateMode(mode)
      const qPath = shQuote(remotePath)
      const script = `cat > ${qPath} && chmod ${mode} ${qPath}`
      const res = exec(script, data)
      if (res.status !== 0) {
        const detail = res.stderr.trim() || `exit code ${res.status}`
        throw new Error(`nextgin deploy: failed to write ${remotePath} on ${o.host}: ${detail}`)
      }
    },

    readFile(remotePath: string): string | undefined {
      const qPath = shQuote(remotePath)
      const res = exec(`if [ -e ${qPath} ]; then cat ${qPath}; else exit 42; fi`)
      if (res.status === 0) {
        return res.stdout
      }
      if (res.status === 42) {
        return undefined
      }
      const detail = res.stderr.trim() || `exit code ${res.status}`
      throw new Error(`nextgin deploy: failed to read ${remotePath} on ${o.host}: ${detail}`)
    },

    loadImage(ref: string, dockerCmd = 'docker'): Promise<void> {
      if (dockerCmd !== 'docker' && dockerCmd !== 'sudo -n docker') {
        throw new Error(`nextgin deploy: invalid docker command "${dockerCmd}"`)
      }
      console.log('[nextgin] uploading image...')
      return new Promise<void>((resolve, reject) => {
        const save = spawn('docker', ['save', ref], {
          stdio: ['ignore', 'pipe', 'pipe'],
        })
        const sshArgs = buildSshArgs(o, `gunzip | ${dockerCmd} load`)
        const ssh = spawn('ssh', sshArgs, {
          stdio: ['pipe', 'ignore', 'pipe'],
        })

        let saveErr = ''
        let sshErr = ''
        save.stderr.on('data', (chunk: Buffer) => {
          saveErr += chunk.toString('utf8')
        })
        ssh.stderr.on('data', (chunk: Buffer) => {
          sshErr += chunk.toString('utf8')
        })

        let settled = false
        const failOnce = (err: Error) => {
          if (settled) return
          settled = true
          save.kill('SIGTERM')
          ssh.kill('SIGTERM')
          reject(err)
        }

        const gzip = createGzip()
        save.stdout.on('error', (e) => failOnce(e))
        gzip.on('error', (e) => failOnce(e))
        ssh.stdin.on('error', (e) => {
          // Ignore EPIPE when ssh exits early; the close handler reports ssh's stderr.
          if ((e as NodeJS.ErrnoException).code !== 'EPIPE') {
            failOnce(e)
          }
        })

        save.stdout.pipe(gzip).pipe(ssh.stdin)

        let saveCode: number | null = null
        let sshCode: number | null = null

        const maybeFinish = () => {
          if (settled || saveCode === null || sshCode === null) return
          settled = true
          if (saveCode !== 0) {
            reject(new Error(`nextgin deploy: docker save failed: ${saveErr.trim() || `exit code ${saveCode}`}`))
            return
          }
          if (sshCode !== 0) {
            reject(new Error(`nextgin deploy: image upload failed: ${sshErr.trim() || `exit code ${sshCode}`}`))
            return
          }
          resolve()
        }

        save.on('error', (e) => failOnce(new Error(`nextgin deploy: docker save failed: ${e.message}`)))
        ssh.on('error', (e) => failOnce(new Error(`nextgin deploy: ssh failed: ${e.message}`)))
        save.on('close', (code) => {
          saveCode = code ?? 1
          maybeFinish()
        })
        ssh.on('close', (code) => {
          sshCode = code ?? 1
          maybeFinish()
        })
      })
    },
  }
}

/**
 * Creates a local Transport executing directly on the current machine.
 */
export function createLocalTransport(): Transport {
  return {
    kind: 'local',
    label: 'this machine',

    exec(script: string): ExecResult {
      const r = spawnSync('sh', ['-c', script], {
        encoding: 'utf8',
        maxBuffer: 16 * 1024 * 1024,
      })
      if (r.error) {
        return {
          status: r.status ?? 1,
          stdout: r.stdout ?? '',
          stderr: r.stderr ? `${r.stderr}\n${r.error.message}` : r.error.message,
        }
      }
      return {
        status: r.status ?? 1,
        stdout: r.stdout ?? '',
        stderr: r.stderr ?? '',
      }
    },

    put(remotePath: string, data: string | Buffer, mode: string): void {
      validateMode(mode)
      mkdirSync(dirname(remotePath), { recursive: true })
      writeFileSync(remotePath, data)
      chmodSync(remotePath, Number.parseInt(mode, 8))
    },

    readFile(remotePath: string): string | undefined {
      try {
        return readFileSync(remotePath, 'utf8')
      } catch (err) {
        if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
          return undefined
        }
        throw err
      }
    },

    loadImage(): void {
      // No-op locally: `docker build` already loaded the image into the local daemon.
    },
  }
}
