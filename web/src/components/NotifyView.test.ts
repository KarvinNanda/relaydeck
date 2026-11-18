import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Message, MessageState, State } from '../types'

// The view uses the app-wide singletons; the API module is the seam.
const api = vi.hoisted(() => vi.fn())
vi.mock('../api', async (orig) => ({ ...(await orig<typeof import('../api')>()), api }))

import { store } from '../stores/state'
import NotifyView from './NotifyView.vue'

function snapshot(): State {
  const f = (name: string) => ({ name, path: `/x/${name}`, permission_mode: 'auto', status: 'idle' as const, on_default_login: false, pending: 0, awaiting_approval: 0 })
  return {
    platform: 'darwin',
    account: { cswap: false, known: false, default: '' },
    workspaces: [{ name: 'game-lounge', folders: [f('be'), f('fe-admin'), f('worker')] }],
  }
}

let n = 0
function msg(state: MessageState, from = 'be', to = 'fe-admin'): Message {
  n++
  return { id: `m${n}`, workspace: 'game-lounge', from, to, body: `body ${n}`, hop: state === 'awaiting_approval' ? 3 : 1, created: '2026-11-17T10:42:05Z', state }
}

let list: Message[] = []

beforeEach(() => {
  list = []
  api.mockReset()
  api.mockImplementation(async (method: string) => (method === 'GET' ? { messages: list } : undefined))
  store.state.connection = 'connected'
  store.state.data = snapshot()
  store.ui.messagesOpen = true
})

afterEach(() => {
  store.state.data = null
  store.state.connection = 'connecting'
})

describe('NotifyView', () => {
  it('loads the active workspace and shows the timeline and the graph', async () => {
    list = [msg('delivered'), msg('queued', 'worker', 'be')]
    const w = mount(NotifyView)
    await flushPromises()
    expect(api).toHaveBeenCalledWith('GET', '/api/workspaces/game-lounge/messages')
    expect(w.findAll('.tl [role="row"]:not(.th)')).toHaveLength(2)
    expect(w.findAll('g.node')).toHaveLength(3)
    expect(w.find('.gr-h').text()).toContain('game-lounge')
    w.unmount()
  })

  it('shows the counts in the header, each with an icon, and hides zero counts', async () => {
    list = [msg('awaiting_approval', 'worker', 'be'), msg('awaiting_approval', 'worker', 'be'), msg('held_external', 'be', 'worker'), msg('delivered')]
    const w = mount(NotifyView)
    await flushPromises()
    const sum = w.findAll('.dh .sum > span')
    expect(sum.map((s) => s.text())).toEqual(['2 awaiting approval', '1 held'])
    for (const s of sum) expect(s.find('svg').exists()).toBe(true)
    w.unmount()
  })

  it('Approve calls the approve endpoint', async () => {
    list = [msg('awaiting_approval', 'worker', 'be')]
    const w = mount(NotifyView)
    await flushPromises()
    await w.find('button.btn-primary').trigger('click')
    await flushPromises()
    expect(api).toHaveBeenCalledWith('POST', `/api/messages/${list[0].id}/approve`)
    w.unmount()
  })

  it('keeps the collapse button, labelled and wired to the drawer', async () => {
    const w = mount(NotifyView)
    const b = w.find('button[aria-label="Collapse Messages"]')
    expect(b.attributes('aria-controls')).toBe('messages')
    expect(b.attributes('aria-expanded')).toBe('true')
    await b.trigger('click')
    expect(store.ui.messagesOpen).toBe(false)
    w.unmount()
  })

  it('stops following events when unmounted', async () => {
    const w = mount(NotifyView)
    await flushPromises()
    w.unmount()
    api.mockClear()
    store.state.connection = 'disconnected'
    await flushPromises()
    store.state.connection = 'connected'
    await flushPromises()
    expect(api).not.toHaveBeenCalled()
  })
})
