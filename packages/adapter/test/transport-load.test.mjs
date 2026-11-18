import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync, chmodSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { createSshTransport } from '../dist/deploy/transport.js'

function fakeBin(dir, name, body) {
  const p = join(dir, name)
  writeFileSync(p, `#!/bin/sh\n${body}\n`)
  chmodSync(p, 0o755)
}

test('loadImage rejects with ssh stderr when the remote load fails, instead of hanging on docker save', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'ng-load-'))
  const oldPath = process.env.PATH
  try {
    // docker save streams far more than a pipe buffer; ssh dies immediately.
    fakeBin(dir, 'docker', 'exec yes')
    fakeBin(dir, 'ssh', 'echo "Cannot connect to the Docker daemon" >&2; exit 1')
    process.env.PATH = `${dir}:${oldPath}`
    const t = createSshTransport({ host: 'u@example.invalid', port: 22 })
    let timer
    const result = await Promise.race([
      t.loadImage('app:1').then(
        () => 'resolved',
        (e) => e,
      ),
      new Promise((r) => {
        timer = setTimeout(() => r('HUNG'), 15000)
      }),
    ])
    clearTimeout(timer)
    assert.notEqual(result, 'HUNG', 'loadImage hung')
    assert.notEqual(result, 'resolved')
    assert.match(String(result.message), /image upload failed: .*Docker daemon/)
  } finally {
    process.env.PATH = oldPath
    rmSync(dir, { recursive: true, force: true })
  }
})
