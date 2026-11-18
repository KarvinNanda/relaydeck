import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, reactive } from 'vue'
import { ApiError } from '../api'
import type { DaemonEvent, Message, MessageState, WorkspaceState } from '../types'
import { createMessages, type MessagesDeps } from './messages'

function msg(id: string, state: MessageState, extra: Partial<Message> = {}): Message {
  return { id, workspace: 'w', from: 'a', to: 'b', body: `body ${id}`, hop: 1, created: '2026-11-17T10:00:00Z', state, ...extra }
}

// rig builds a messages store on a fake state store and a fake API. answers
// holds one entry per GET; each is resolved by the test (so it can race).
function rig(opts: { ws?: string | null; connection?: 'connecting' | 'connected' | 'disconnected' } = {}) {
  const listeners = new Set<(e: DaemonEvent) => void>()
  const ui = reactive({ ws: opts.ws === undefined ? 'w' : opts.ws })
  const state = reactive({ connection: opts.connection ?? 'connected' })
  const gets: { path: string; resolve: (v: unknown) => void; reject: (e: unknown) => void }[] = []
  const posts: { path: string; body?: unknown }[] = []
  const api = vi.fn((method: string, path: string, body?: unknown): Promise<unknown> => {
    if (method === 'GET') return new Promise((resolve, reject) => gets.push({ path, resolve, reject }))
    posts.push({ path, body })
    return Promise.resolve(undefined)
  })
  const store = {
    state,
    activeWorkspace: (): WorkspaceState | null => (ui.ws ? { name: ui.ws, folders: [] } : null),
    onEvent: (fn: (e: DaemonEvent) => void) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
  }
  const m = createMessages({ api, store } as unknown as MessagesDeps)
  const emit = (message: Message, ws = message.workspace) => {
    for (const fn of listeners) fn({ type: 'message', ws, folder: message.to, message })
  }
  return { m, api, gets, posts, ui, state, emit, listeners }
}

const flush = () => vi.advanceTimersByTimeAsync(0)

describe('messages store', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('loads the timeline of the active workspace and lists it newest first', async () => {
    const { m, gets } = rig()
    m.start()
    expect(gets.map((g) => g.path)).toEqual(['/api/workspaces/w/messages'])
    gets[0].resolve({ messages: [msg('1', 'delivered'), msg('2', 'queued')] })
    await flush()
    expect(m.newestFirst.value.map((x) => x.id)).toEqual(['2', '1'])
    expect(m.loading.value).toBe(false)
  })

  it('escapes the workspace name in the path', () => {
    const { m, gets } = rig({ ws: 'a b/c' })
    m.start()
    expect(gets[0].path).toBe('/api/workspaces/a%20b%2Fc/messages')
  })

  it('applies message events: a new id is added, a known id is replaced', async () => {
    const { m, gets, emit } = rig()
    m.start()
    gets[0].resolve({ messages: [msg('1', 'held_draft')] })
    await flush()
    emit(msg('1', 'delivered'))
    emit(msg('2', 'queued'))
    expect(m.newestFirst.value.map((x) => [x.id, x.state])).toEqual([
      ['2', 'queued'],
      ['1', 'delivered'],
    ])
  })

  it('ignores events of another workspace', async () => {
    const { m, gets, emit } = rig()
    m.start()
    gets[0].resolve({ messages: [] })
    await flush()
    emit(msg('9', 'queued', { workspace: 'other' }))
    expect(m.newestFirst.value).toEqual([])
  })

  it('keeps events that arrive while a load is in flight', async () => {
    const { m, gets, emit } = rig()
    m.start()
    emit(msg('1', 'delivered'))
    emit(msg('2', 'queued'))
    // The answer was taken before both events.
    gets[0].resolve({ messages: [msg('1', 'nudged')] })
    await flush()
    expect(m.newestFirst.value.map((x) => [x.id, x.state])).toEqual([
      ['2', 'queued'],
      ['1', 'delivered'],
    ])
  })

  it('reloads when the connection goes back to connected', async () => {
    const { m, gets, state } = rig()
    m.start()
    gets[0].resolve({ messages: [msg('1', 'queued')] })
    await flush()
    state.connection = 'disconnected'
    await nextTick()
    expect(gets).toHaveLength(1)
    state.connection = 'connected'
    await nextTick()
    expect(gets).toHaveLength(2)
    gets[1].resolve({ messages: [msg('1', 'delivered'), msg('2', 'held_external')] })
    await flush()
    expect(m.newestFirst.value.map((x) => x.state)).toEqual(['held_external', 'delivered'])
  })

  it('switches workspace: drops the old list and drops a stale answer', async () => {
    const { m, gets, ui } = rig()
    m.start()
    ui.ws = 'v'
    await nextTick()
    expect(gets.map((g) => g.path)).toEqual(['/api/workspaces/w/messages', '/api/workspaces/v/messages'])
    gets[1].resolve({ messages: [msg('5', 'queued', { workspace: 'v' })] })
    await flush()
    gets[0].resolve({ messages: [msg('1', 'queued')] })
    await flush()
    expect(m.newestFirst.value.map((x) => x.id)).toEqual(['5'])
  })

  it('shows an empty list with no workspace and does not call the API', () => {
    const { m, api } = rig({ ws: null })
    m.start()
    expect(api).not.toHaveBeenCalled()
    expect(m.newestFirst.value).toEqual([])
  })

  it('keeps the load error as text', async () => {
    const { m, gets } = rig()
    m.start()
    gets[0].reject(new ApiError(404, 'workspace "w" not found'))
    await flush()
    expect(m.error.value).toBe('workspace "w" not found')
  })

  it('counts awaiting approval, held and waiting', async () => {
    const { m, gets } = rig()
    m.start()
    gets[0].resolve({
      messages: [
        msg('1', 'awaiting_approval'),
        msg('2', 'awaiting_approval'),
        msg('3', 'held_external'),
        msg('4', 'held_draft'),
        msg('5', 'queued'),
        msg('6', 'delivered'),
      ],
    })
    await flush()
    expect(m.counts.value).toEqual({ awaiting: 2, held: 1, waiting: 1 })
  })

  it('lights a message for 2 s after each event', async () => {
    const { m, gets, emit } = rig()
    m.start()
    gets[0].resolve({ messages: [msg('1', 'queued')] })
    await flush()
    expect(m.lit.has('1')).toBe(false)
    emit(msg('1', 'nudged'))
    expect(m.lit.has('1')).toBe(true)
    await vi.advanceTimersByTimeAsync(1500)
    emit(msg('1', 'delivered')) // a second event restarts the 2 s
    await vi.advanceTimersByTimeAsync(1500)
    expect(m.lit.has('1')).toBe(true)
    await vi.advanceTimersByTimeAsync(600)
    expect(m.lit.has('1')).toBe(false)
  })

  it('stop removes the listener, the watchers and the light timers', async () => {
    const { m, gets, state, listeners, emit } = rig()
    m.start()
    emit(msg('1', 'queued'))
    expect(m.lit.size).toBe(1)
    expect(vi.getTimerCount()).toBe(1)
    m.stop()
    expect(m.lit.size).toBe(0)
    expect(vi.getTimerCount()).toBe(0)
    expect(listeners.size).toBe(0)
    state.connection = 'disconnected'
    await nextTick()
    state.connection = 'connected'
    await nextTick()
    expect(gets).toHaveLength(1)
  })

  it('posts the row actions to the right endpoints', async () => {
    const { m, posts } = rig()
    const held = msg('m 1', 'held_external', { workspace: 'w s', to: 'fe/x' })
    await m.approve('m 1')
    await m.reject('m 1')
    await m.startNew(held)
    await m.deliverNow(held)
    expect(posts).toEqual([
      { path: '/api/messages/m%201/approve', body: undefined },
      { path: '/api/messages/m%201/reject', body: undefined },
      { path: '/api/agents/w%20s/fe%2Fx/start', body: { new: true } },
      { path: '/api/agents/w%20s/fe%2Fx/deliver-now', body: undefined },
    ])
  })
})
