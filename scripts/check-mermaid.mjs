// Parses every ```mermaid block in the given markdown files with the real
// mermaid parser. Exit 0 when every block parses. Exit 1 when a block is
// broken, a file has no block, or a file cannot be read.
// Usage: node scripts/check-mermaid.mjs [file.md ...]   (default: docs/architecture.md)
// Needs `npm ci` in web/ first: mermaid and jsdom come from web/node_modules.
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = fileURLToPath(new URL('../', import.meta.url))
const webRequire = createRequire(resolve(root, 'web/package.json'))

// mermaid sanitizes text with DOMPurify, which needs a DOM. Install jsdom's
// window as globals before mermaid loads.
const { JSDOM } = webRequire('jsdom')
const dom = new JSDOM('<!doctype html><html><body></body></html>')
globalThis.window = dom.window
globalThis.document = dom.window.document
for (const k of ['Element', 'HTMLElement', 'SVGElement', 'Node', 'DOMParser', 'XMLSerializer']) {
  if (!(k in globalThis)) globalThis[k] = dom.window[k]
}

const mermaid = (await import(pathToFileURL(webRequire.resolve('mermaid')).href)).default
mermaid.initialize({ startOnLoad: false })

// blocks returns the fenced mermaid blocks of a markdown text, with the line
// number where each block's code starts.
function blocks(text) {
  const out = []
  const lines = text.split('\n')
  for (let i = 0; i < lines.length; i++) {
    if (!/^```mermaid\s*$/.test(lines[i])) continue
    const start = i + 2
    const body = []
    for (i++; i < lines.length && !/^```\s*$/.test(lines[i]); i++) body.push(lines[i])
    out.push({ line: start, code: body.join('\n') })
  }
  return out
}

const files = process.argv.length > 2 ? process.argv.slice(2) : [resolve(root, 'docs/architecture.md')]
let failed = 0
let total = 0
let ok = 0
for (const file of files) {
  let text
  try {
    text = readFileSync(file, 'utf8')
  } catch (e) {
    console.error(`FAIL ${file}: cannot read (${e.message})`)
    failed++
    continue
  }
  const found = blocks(text)
  if (found.length === 0) {
    console.error(`FAIL ${file}: no mermaid blocks found`)
    failed++
    continue
  }
  for (const b of found) {
    total++
    try {
      await mermaid.parse(b.code)
      ok++
      console.log(`ok   ${file}:${b.line}`)
    } catch (e) {
      failed++
      console.error(`FAIL ${file}:${b.line}: ${String(e.message ?? e).split('\n').slice(0, 4).join(' | ')}`)
    }
  }
}
console.log(`check-mermaid: ${ok} of ${total} blocks ok`)
process.exit(failed > 0 ? 1 : 0)
