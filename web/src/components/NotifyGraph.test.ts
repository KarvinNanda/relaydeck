import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'
import { createMessages, type MessagesDeps } from '../stores/messages'
import type { AgentStatus, DaemonEvent, FolderState, Message, MessageState } from '../types'
import NotifyGraph from './NotifyGraph.vue'

function folder(name: string, status: AgentStatus): FolderState {
  return { name, path: `/x/${name}`, permission_mode: 'auto', status, on_default_login: false, pending: 0, awaiting_approval: 0 }
}

const folders = [folder('be', 'busy'), folder('fe-admin', 'idle'), folder('worker', 'stopped'), folder('fe-customer', 'attention')]

let n = 0
function msg(from: string, to: string, state: MessageState, at = new Date(2026, 10, 17, 10, 31, 20)): Message {
  n++
  return { id: `m${n}`, workspace: 'w', from, to, body: 'x', hop: 1, created: at.toISOString(), state }
}

async function setup(list: Message[]) {
  const listeners = new Set<(e: DaemonEvent) => void>()
  const api = vi.fn(() => Promise.resolve({ messages: list }))
  const store = {
    state: reactive({ connection: 'connected' }),
    activeWorkspace: () => ({ name: 'w', folders }),
    onEvent: (fn: (e: DaemonEvent) => void) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
  }
  const m = createMessages({ api, store } as unknown as MessagesDeps)
  m.start()
  await flushPromises()
  const w = mount(NotifyGraph, { props: { messages: m, folders, workspace: 'w' } })
  const emit = (x: Message) => {
    for (const fn of listeners) fn({ type: 'message', ws: 'w', folder: x.to, message: x })
  }
  return { w, m, emit }
}

const edge = (w: ReturnType<typeof mount>, from: string, to: string) => w.find(`path.edge[data-from="${from}"][data-to="${to}"]`)
const pairs = (w: ReturnType<typeof mount>) => w.findAll('path.edge').map((e) => `${e.attributes('data-from')}>${e.attributes('data-to')}`)

// Text in the graph is drawn at scale 1 with JetBrains Mono: 0.6 em per character.
const charW = 14 * 0.6

describe('NotifyGraph', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('draws one node per folder with its name and a status icon', async () => {
    const { w } = await setup([])
    const nodes = w.findAll('g.node')
    expect(nodes.map((g) => g.find('text').text())).toEqual(['be', 'fe-admin', 'worker', 'fe-customer'])
    for (const g of nodes) expect(g.find('svg.i').exists()).toBe(true)
    expect(nodes[3].find('title').text()).toBe('fe-customer: needs attention')
    expect(nodes[3].classes()).toContain('c-attn')
  })

  it('makes every node wide enough for its label plus padding', async () => {
    const { w } = await setup([])
    for (const g of w.findAll('g.node')) {
      const rect = g.find('rect')
      const text = g.find('text')
      const textEnd = Number(text.attributes('x')) + text.text().length * charW
      expect(textEnd + 8).toBeLessThanOrEqual(Number(rect.attributes('x')) + Number(rect.attributes('width')))
    }
  })

  it('measures the real text width when the browser can, instead of assuming 0.6 em', async () => {
    // jsdom has no layout: give the probe span 12 px per character.
    const spy = vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      const w = this.classList.contains('probe') ? (this.textContent ?? '').length * 12 : 0
      return { width: w, height: 0, top: 0, left: 0, right: w, bottom: 0, x: 0, y: 0, toJSON() {} } as DOMRect
    })
    try {
      const { w } = await setup([])
      for (const g of w.findAll('g.node')) {
        const text = g.find('text')
        const textEnd = Number(text.attributes('x')) + text.text().length * 12
        expect(textEnd + 8).toBeLessThanOrEqual(Number(g.find('rect').attributes('x')) + Number(g.find('rect').attributes('width')))
      }
    } finally {
      spy.mockRestore()
    }
  })

  it('draws text at 14 units and never scales the SVG below 1', async () => {
    const { w } = await setup([])
    const svg = w.find('svg.graph')
    const [, , vw, vh] = svg.attributes('viewBox')!.split(' ').map(Number)
    expect(Number(svg.attributes('width'))).toBe(vw)
    expect(Number(svg.attributes('height'))).toBe(vh)
    for (const t of svg.findAll('text')) expect(Number(t.attributes('font-size'))).toBeGreaterThanOrEqual(14)
  })

  it('draws one edge per from/to pair seen in the timeline', async () => {
    const { w } = await setup([
      msg('be', 'worker', 'delivered'),
      msg('be', 'worker', 'delivered'),
      msg('worker', 'be', 'delivered'),
      msg('be', 'gone', 'delivered'), // a folder no longer in the workspace
    ])
    expect(pairs(w)).toEqual(['be>worker', 'worker>be'])
  })

  it('edge style carries the state: solid delivered, dashed held, dotted approval', async () => {
    const { w } = await setup([
      msg('be', 'worker', 'delivered'),
      msg('fe-customer', 'worker', 'delivered'),
      msg('fe-customer', 'worker', 'held_external'),
      msg('worker', 'be', 'held_draft'),
      msg('worker', 'be', 'awaiting_approval'),
      msg('be', 'worker', 'rejected'),
    ])
    expect(edge(w, 'be', 'worker').classes()).toContain('solid')
    expect(edge(w, 'fe-customer', 'worker').classes()).toContain('dashed')
    // the pair shows the state that needs the user most
    expect(edge(w, 'worker', 'be').classes()).toContain('dotted')
  })

  it('skips rejected messages: a pair with only rejected messages has no edge', async () => {
    const { w } = await setup([msg('be', 'fe-admin', 'rejected'), msg('be', 'worker', 'delivered')])
    expect(pairs(w)).toEqual(['be>worker'])
  })

  it('lights the edge of a new message event for 2 s', async () => {
    const { w, emit } = await setup([msg('be', 'worker', 'delivered'), msg('worker', 'be', 'delivered')])
    expect(edge(w, 'be', 'worker').classes()).not.toContain('lit')
    emit(msg('be', 'worker', 'queued'))
    await flushPromises()
    expect(edge(w, 'be', 'worker').classes()).toContain('lit')
    await vi.advanceTimersByTimeAsync(2000)
    expect(edge(w, 'be', 'worker').classes()).not.toContain('lit')
  })

  it('does not light or label an edge for a rejected message', async () => {
    const { w, emit } = await setup([msg('be', 'worker', 'delivered', new Date(2026, 10, 17, 10, 31, 20))])
    emit(msg('worker', 'be', 'rejected', new Date(2026, 10, 17, 10, 50, 0)))
    emit({ ...msg('be', 'worker', 'rejected'), id: 'other' })
    await flushPromises()
    expect(edge(w, 'be', 'worker').classes()).not.toContain('lit')
    expect(w.find('text.when').text()).toBe('10:31')
  })

  it('marks the latest edge and labels it with its time, clear of every node', async () => {
    const { w } = await setup([
      msg('be', 'worker', 'delivered', new Date(2026, 10, 17, 10, 31, 20)),
      msg('be', 'fe-admin', 'held_draft', new Date(2026, 10, 17, 10, 42, 5)),
    ])
    expect(edge(w, 'be', 'fe-admin').classes()).toContain('latest')
    expect(edge(w, 'be', 'worker').classes()).not.toContain('latest')
    const label = w.find('text.when')
    expect(label.text()).toBe('10:42')
    expect([label.attributes('data-from'), label.attributes('data-to')]).toEqual(['be', 'fe-admin'])
    // the label box, from its anchor, must keep clear of every node box
    const lw = label.text().length * charW
    const lx = Number(label.attributes('x'))
    const ly = Number(label.attributes('y'))
    const anchor = label.attributes('text-anchor')
    const left = anchor === 'middle' ? lx - lw / 2 : anchor === 'end' ? lx - lw : lx
    const box = { x1: left, x2: left + lw, y1: ly - 7, y2: ly + 7 } // dominant-baseline: middle
    for (const r of w.findAll('g.node rect')) {
      const x = Number(r.attributes('x'))
      const y = Number(r.attributes('y'))
      const nb = { x1: x - 2, x2: x + Number(r.attributes('width')) + 2, y1: y - 2, y2: y + Number(r.attributes('height')) + 2 }
      const overlap = box.x1 < nb.x2 && box.x2 > nb.x1 && box.y1 < nb.y2 && box.y2 > nb.y1
      expect(overlap).toBe(false)
    }
  })

  it('draws no time label when no spot clears the nodes, and shows the time in the legend', async () => {
    const two = [folder('be', 'busy'), folder('fe-admin', 'idle')]
    const spy = vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      const w = this.classList.contains('probe') ? (this.textContent ?? '').length * 12 : 0
      return { width: w, height: 0, top: 0, left: 0, right: w, bottom: 0, x: 0, y: 0, toJSON() {} } as DOMRect
    })
    try {
      const { m } = await setup([msg('be', 'fe-admin', 'held_draft', new Date(2026, 10, 17, 10, 42, 5))])
      const w = mount(NotifyGraph, { props: { messages: m, folders: two, workspace: 'w' } })
      await flushPromises()
      expect(w.find('text.when').exists()).toBe(false)
      expect(w.find('.gl > span').text()).toBe('latest 10:42')
    } finally {
      spy.mockRestore()
    }
  })

  it('keeps pairs apart even when folder names contain the separator', async () => {
    const odd = [folder('a>b', 'idle'), folder('c', 'idle'), folder('a', 'idle'), folder('b>c', 'idle')]
    const { m } = await setup([
      { ...msg('a>b', 'c', 'delivered'), from: 'a>b', to: 'c' },
      { ...msg('a', 'b>c', 'held_draft'), from: 'a', to: 'b>c' },
    ])
    const w = mount(NotifyGraph, { props: { messages: m, folders: odd, workspace: 'w' } })
    expect(pairs(w)).toEqual(['a>b>c', 'a>b>c'])
    expect(edge(w, 'a>b', 'c').classes()).toContain('solid')
    expect(edge(w, 'a', 'b>c').classes()).toContain('dashed')
  })

  it('has a legend that names every edge style, with the same word as the label', async () => {
    const { w } = await setup([])
    const items = w.findAll('.gl > span')
    expect(items.map((s) => s.text())).toEqual(['latest', 'delivered', 'held', 'approval'])
    expect(items.map((s) => s.find('line').classes().filter((c) => c !== 'edge'))).toEqual([
      ['solid', 'latest'],
      ['solid'],
      ['dashed'],
      ['dotted'],
    ])
  })

  it('shows "latest" in the legend as a green bar only, not a line pattern', async () => {
    const { w } = await setup([])
    const lines = w.findAll('.gl > span')[0].findAll('line')
    expect(lines).toHaveLength(1)
    expect(lines[0].classes().filter((c) => c !== 'edge')).toEqual(['solid', 'latest'])
    // the pattern swatches are not "latest", so they are neutral
    for (const s of w.findAll('.gl > span').slice(1)) expect(s.find('line').classes()).not.toContain('latest')
  })

  it('says so when the workspace has no folders', async () => {
    const { m } = await setup([])
    const w = mount(NotifyGraph, { props: { messages: m, folders: [], workspace: 'w' } })
    expect(w.find('svg.graph').exists()).toBe(false)
    expect(w.text()).toContain('No folders in this workspace.')
  })
})
