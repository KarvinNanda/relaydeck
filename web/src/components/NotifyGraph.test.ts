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

const edge = (w: ReturnType<typeof mount>, from: string, to: string) => w.find(`[data-edge="${from}>${to}"]`)

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

  it('draws one edge per from/to pair seen in the timeline', async () => {
    const { w } = await setup([
      msg('be', 'worker', 'delivered'),
      msg('be', 'worker', 'delivered'),
      msg('worker', 'be', 'delivered'),
      msg('be', 'gone', 'delivered'), // a folder no longer in the workspace
    ])
    expect(w.findAll('[data-edge]').map((e) => e.attributes('data-edge'))).toEqual(['be>worker', 'worker>be'])
  })

  it('edge style carries the state: solid delivered, dashed held, dotted approval', async () => {
    const { w } = await setup([
      msg('be', 'worker', 'delivered'),
      msg('fe-customer', 'worker', 'delivered'),
      msg('fe-customer', 'worker', 'held_external'),
      msg('worker', 'be', 'held_draft'),
      msg('worker', 'be', 'awaiting_approval'),
      msg('be', 'fe-admin', 'rejected'),
    ])
    expect(edge(w, 'be', 'worker').classes()).toContain('solid')
    expect(edge(w, 'fe-customer', 'worker').classes()).toContain('dashed')
    // the pair shows the state that needs the user most
    expect(edge(w, 'worker', 'be').classes()).toContain('dotted')
    expect(edge(w, 'be', 'fe-admin').classes()).toContain('solid')
  })

  it('lights the edge of a new message event for 2 s', async () => {
    const { w, emit } = await setup([msg('be', 'worker', 'delivered')])
    expect(edge(w, 'be', 'worker').classes()).not.toContain('lit')
    emit(msg('be', 'worker', 'queued'))
    await flushPromises()
    expect(edge(w, 'be', 'worker').classes()).toContain('lit')
    await vi.advanceTimersByTimeAsync(2000)
    expect(edge(w, 'be', 'worker').classes()).not.toContain('lit')
  })

  it('labels the latest edge with its time', async () => {
    const { w } = await setup([
      msg('be', 'worker', 'delivered', new Date(2026, 10, 17, 10, 31, 20)),
      msg('be', 'fe-admin', 'held_draft', new Date(2026, 10, 17, 10, 42, 5)),
    ])
    const labels = w.findAll('text.when')
    expect(labels).toHaveLength(1)
    expect(labels[0].text()).toBe('10:42 latest')
    expect(labels[0].attributes('data-for')).toBe('be>fe-admin')
  })

  it('has a legend that names every edge style', async () => {
    const { w } = await setup([])
    const items = w.findAll('.gl > span')
    expect(items.map((s) => s.text())).toEqual(['new (2 s)', 'delivered', 'held', 'approval'])
    expect(items.map((s) => s.find('line').classes().filter((c) => c !== 'edge'))).toEqual([
      ['solid', 'lit'],
      ['solid'],
      ['dashed'],
      ['dotted'],
    ])
  })

  it('says so when the workspace has no folders', async () => {
    const { m } = await setup([])
    const w = mount(NotifyGraph, { props: { messages: m, folders: [], workspace: 'w' } })
    expect(w.find('svg.graph').exists()).toBe(false)
    expect(w.text()).toContain('No folders in this workspace.')
  })
})
