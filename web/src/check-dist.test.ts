// @vitest-environment node
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it } from 'vitest'

const script = fileURLToPath(new URL('../scripts/check-dist.mjs', import.meta.url))
const good = '<!doctype html><html><head><script type="module" crossorigin src="/assets/index-abc.js"></script></head><body><div id="app"></div></body></html>'

const dirs: string[] = []
afterEach(() => {
  for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true })
})

// fixture writes a fake dist dir and returns it.
function fixture(html: string, gitkeep = true): string {
  const dir = mkdtempSync(join(tmpdir(), 'check-dist-'))
  dirs[dirs.length] = dir
  mkdirSync(join(dir, 'assets'))
  writeFileSync(join(dir, 'index.html'), html)
  writeFileSync(join(dir, 'assets', 'index-abc.css'), 'body{font-family:x}')
  if (gitkeep) writeFileSync(join(dir, '.gitkeep'), '')
  return dir
}

function run(dir: string): { code: number; out: string } {
  try {
    const out = execFileSync(process.execPath, [script, dir], { encoding: 'utf8', stdio: 'pipe' })
    return { code: 0, out }
  } catch (e) {
    const err = e as { status: number; stdout: string; stderr: string }
    return { code: err.status, out: err.stdout + err.stderr }
  }
}

describe('check-dist', () => {
  it('passes a clean build', () => {
    expect(run(fixture(good))).toMatchObject({ code: 0 })
  })

  it('fails on an inline script', () => {
    const r = run(fixture(good.replace('<div id="app"></div>', '<div id="app"></div><script>alert(1)</script>')))
    expect(r.code).toBe(1)
    expect(r.out).toContain('inline or non-module script')
  })

  it('fails on an uppercase inline SCRIPT', () => {
    const r = run(fixture(good.replace('<div id="app"></div>', '<div id="app"></div><SCRIPT>alert(1)</SCRIPT>')))
    expect(r.code).toBe(1)
    expect(r.out).toContain('inline or non-module script: <SCRIPT>')
  })

  it('fails on a script from another origin', () => {
    const r = run(fixture(good.replace('src="/assets/index-abc.js"', 'src="https://cdn.example/x.js"')))
    expect(r.code).toBe(1)
    expect(r.out).toContain('loads something from another origin')
  })

  it('fails when .gitkeep is missing', () => {
    const r = run(fixture(good, false))
    expect(r.code).toBe(1)
    expect(r.out).toContain('.gitkeep is missing')
  })
})
