import { describe, it } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { scaffoldProject } from '../dist/init.js'

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')

function makeTempDir(prefix) {
  return fs.mkdtempSync(path.join(os.tmpdir(), prefix))
}

function wireLocalNextgin(goModPath) {
  const existing = fs.readFileSync(goModPath, 'utf8')
  fs.writeFileSync(goModPath, `${existing}\nreplace github.com/Chidi09/nextgin => ${repoRoot}\n`, 'utf8')
  const tidy = spawnSync('go', ['mod', 'tidy'], { cwd: path.dirname(goModPath), encoding: 'utf8' })
  assert.equal(tidy.status, 0, `go mod tidy failed:\n${tidy.stdout}\n${tidy.stderr}`)
}

describe('nextgin init scaffolder', () => {
  it('scaffolds selfhost layout and generated Go tests pass', () => {
    const dir = path.join(makeTempDir('nextgin-init-selfhost-'), 'my-shop')
    const res = scaffoldProject({ targetDir: dir, layout: 'selfhost' })

    assert.equal(res.layout, 'selfhost')
    assert.equal(res.name, 'my-shop')
    assert.equal(res.modulePath, 'example.com/my-shop')
    assert.ok(fs.existsSync(path.join(dir, 'main.go')))
    assert.ok(fs.existsSync(path.join(dir, 'internal/domain/errors.go')))
    assert.ok(fs.existsSync(path.join(dir, 'internal/store/memory.go')))
    assert.ok(fs.existsSync(path.join(dir, 'internal/service/item_service.go')))
    assert.ok(fs.existsSync(path.join(dir, 'internal/httpapi/handlers.go')))
    assert.ok(fs.existsSync(path.join(dir, 'internal/httpapi/handlers_test.go')))
    assert.ok(fs.existsSync(path.join(dir, 'lib/api.ts')))
    assert.ok(fs.existsSync(path.join(dir, 'AGENTS.md')))

    wireLocalNextgin(path.join(dir, 'go.mod'))
    const r = spawnSync('go', ['test', '-count=1', './...'], { cwd: dir, encoding: 'utf8' })
    assert.equal(r.status, 0, `go test failed:\n${r.stdout}\n${r.stderr}`)
  })

  it('scaffolds split layout (web/ + backend/) and generated Go tests pass', () => {
    const dir = path.join(makeTempDir('nextgin-init-split-'), 'acme-web')
    const res = scaffoldProject({
      targetDir: dir,
      layout: 'split',
      modulePath: 'github.com/acme/backend',
    })

    assert.equal(res.layout, 'split')
    assert.equal(res.modulePath, 'github.com/acme/backend')
    assert.ok(fs.existsSync(path.join(dir, 'backend/cmd/server/main.go')))
    assert.ok(fs.existsSync(path.join(dir, 'backend/internal/httpapi/handlers_test.go')))
    assert.ok(fs.existsSync(path.join(dir, 'web/lib/api.ts')))
    assert.ok(fs.existsSync(path.join(dir, 'web/package.json')))

    const backendDir = path.join(dir, 'backend')
    wireLocalNextgin(path.join(backendDir, 'go.mod'))
    const r = spawnSync('go', ['test', '-count=1', './...'], { cwd: backendDir, encoding: 'utf8' })
    assert.equal(r.status, 0, `go test failed:\n${r.stdout}\n${r.stderr}`)
  })

  it('refuses non-empty target directory unless --force is set', () => {
    const dir = makeTempDir('nextgin-init-nonempty-')
    fs.writeFileSync(path.join(dir, 'existing.txt'), 'hi')

    assert.throws(() => scaffoldProject({ targetDir: dir }), /not empty/)
    const res = scaffoldProject({ targetDir: dir, force: true })
    assert.ok(res.files.length > 0)
  })

  it('rejects invalid layout', () => {
    const dir = makeTempDir('nextgin-init-badlayout-')
    assert.throws(() => scaffoldProject({ targetDir: dir, layout: 'invalid' }), /invalid --layout/)
  })
})
