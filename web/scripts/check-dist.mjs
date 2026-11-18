// Checks the built UI against the CSP that internal/server sends
// (script-src 'self', font-src 'self'), and that the Go embed directory
// keeps its .gitkeep. Run by `npm run build`; exits 1 on any problem.
// Usage: node scripts/check-dist.mjs [dist-dir]
// (default: internal/webui/dist; the test passes fixture dirs).
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const dist = process.argv[2]
  ? resolve(process.argv[2])
  : fileURLToPath(new URL('../../internal/webui/dist/', import.meta.url))
const problems = []

const html = readFileSync(join(dist, 'index.html'), 'utf8')
const scripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/gi)]
if (scripts.length === 0) problems[problems.length] = 'index.html has no <script>'
// A <script> without a matching close tag would slip past the loop below.
if ((html.match(/<script\b/gi) ?? []).length !== scripts.length) problems[problems.length] = 'index.html has an unclosed <script>'
for (const [tag, attrs, body] of scripts) {
  if (!/\btype="module"/i.test(attrs) || !/\bsrc="\/(?!\/)[^"]+"/i.test(attrs) || body.trim() !== '') {
    problems[problems.length] = `inline or non-module script: ${tag.slice(0, 120)}`
  }
}
if (/\son[a-z]+\s*=/i.test(html)) problems[problems.length] = 'index.html has an inline event handler attribute'
if (/(?:href|src)="(?:https?:)?\/\//i.test(html)) problems[problems.length] = 'index.html loads something from another origin'

const assets = join(dist, 'assets')
for (const name of existsSync(assets) ? readdirSync(assets) : []) {
  if (!name.endsWith('.css')) continue
  const css = readFileSync(join(assets, name), 'utf8')
  if (/url\(\s*["']?data:font/i.test(css)) problems[problems.length] = `${name} inlines a font as a data: URL (blocked by font-src 'self')`
  if (/url\(\s*["']?(?:https?:)?\/\//i.test(css)) problems[problems.length] = `${name} loads something from another origin`
}

if (!existsSync(join(dist, '.gitkeep'))) problems[problems.length] = `${join(dist, '.gitkeep')} is missing (the Go embed needs it)`

if (problems.length > 0) {
  console.error('check-dist: FAIL\n- ' + problems.join('\n- '))
  process.exit(1)
}
console.log(`check-dist: ok (${scripts.length} module script, no inline code, .gitkeep kept)`)
