import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import type { DaemonEvent, State } from '../types'
import { createStore, type StoreDeps } from './state'

function snapshot(): State {
  return {
    platform: 'darwin',
    account: { cswap: false, known: false, default: '' },
    workspaces: [
      {
        name: 'w',
        folders: [
          { name: 'a', path: '/x/a', permission_mode: 'auto', status: 'idle', on_default_login: false, pending: 0, awaiting_approval: 0 },
          { name: 'b', path: '/x/b', permission_mode: 'auto', status: 'stopped', on_default_login: false, pending: 0, awaiting_approval: 0 },
        ],
      },
    ],
  }
}

// FakeSocket stands in for an open WebSocket on /ws/events.
class FakeSocket {
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: ((ev: CloseEvent) => void) | null = null
  onerror: ((ev: Event) => void) | null = null
  closed = false
  send(frame: object) {
    this.onmessage?.(new MessageEvent('message', { data: JSON.stringify(frame) }))
  }
  drop() {
    this.onclose?.(new CloseEvent('close', { code: 1001 }))
  }
  close() {
    this.closed = true
  }
}

function rig(states: State[] = [snapshot()]) {
  const sockets: FakeSocket[] = []
  let call = 0
  const api = vi.fn(async (): Promise<State> => structuredClone(states[Math.min(call++, states.length - 1)]))
  const openSocket = vi.fn(async (): Promise<WebSocket> => {
    const s = new FakeSocket()
    sockets[sockets.length] = s
    return s as unknown as WebSocket
  })
  const store = createStore({ api, openSocket } as unknown as StoreDeps)
  return { store, api, openSocket, sockets }
}

const flush = () => vi.advanceTimersByTimeAsync(0)

describe('state store', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('fills the state from GET /api/state, then goes connected on the first frame', async () => {
    const { store, api, openSocket, sockets } = rig()
    expect(store.state.connection).toBe('connecting')
    store.start()
    await flush()
    expect(api).toHaveBeenCalledWith('GET', '/api/state')
    expect(store.state.data?.workspaces[0].folders.map((f) => f.name)).toEqual(['a', 'b'])
    expect(openSocket).toHaveBeenCalledWith('/ws/events')
    expect(store.state.connection).toBe('connecting')
    sockets[0].send({ type: 'state', ...snapshot() })
    expect(store.state.connection).toBe('connected')
    expect(store.activeWorkspace()?.name).toBe('w')
  })

  it('applies a status event to one folder only', async () => {
    const { store, sockets } = rig()
    store.start()
    await flush()
    sockets[0].send({ type: 'state', ...snapshot() })
    sockets[0].send({ type: 'status', ws: 'w', folder: 'b', status: 'busy' })
    const [a, b] = store.state.data!.workspaces[0].folders
    expect(b.status).toBe('busy')
    expect(a.status).toBe('idle')
  })

  it('clears the screen field when a folder stops', async () => {
    const { store, sockets } = rig()
    store.start()
    await flush()
    const s = snapshot()
    s.workspaces[0].folders[0].screen = 'trust'
    sockets[0].send({ type: 'state', ...s })
    sockets[0].send({ type: 'status', ws: 'w', folder: 'a', status: 'stopped', exit_code: 0 })
    expect(store.state.data!.workspaces[0].folders[0].status).toBe('stopped')
    expect(store.state.data!.workspaces[0].folders[0].screen).toBeUndefined()
  })

  it('applies an account event', async () => {
    const { store, sockets } = rig()
    store.start()
    await flush()
    sockets[0].send({ type: 'state', ...snapshot() })
    sockets[0].send({ type: 'account', account: { cswap: true, known: true, default: '2' } })
    expect(store.state.data!.account.default).toBe('2')
  })

  it('sets disconnected when the socket drops, then reconnects with backoff', async () => {
    const { store, openSocket, sockets } = rig()
    store.start()
    await flush()
    sockets[0].send({ type: 'state', ...snapshot() })
    sockets[0].drop()
    expect(store.state.connection).toBe('disconnected')
    expect(openSocket).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(499)
    expect(openSocket).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(openSocket).toHaveBeenCalledTimes(2)
    // The new socket's snapshot replaces the state.
    const s = snapshot()
    s.workspaces[0].folders[1].status = 'idle'
    sockets[1].send({ type: 'state', ...s })
    expect(store.state.connection).toBe('connected')
    expect(store.state.data!.workspaces[0].folders[1].status).toBe('idle')
  })

  it('waits longer after each failed reconnect', async () => {
    const { store, openSocket, sockets } = rig()
    store.start()
    await flush()
    openSocket.mockRejectedValue(new ApiError(0, 'down'))
    sockets[0].drop()
    await vi.advanceTimersByTimeAsync(500) // attempt 2 fails
    expect(openSocket).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(999)
    expect(openSocket).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1) // attempt 3 after 1 s
    expect(openSocket).toHaveBeenCalledTimes(3)
    expect(store.state.connection).toBe('disconnected')
  })

  it('caps the reconnect delay at 10 s', async () => {
    const { store, openSocket, sockets } = rig()
    store.start()
    await flush()
    openSocket.mockRejectedValue(new ApiError(0, 'down'))
    sockets[0].drop()
    // Attempts 2 to 7 come after 0.5, 1, 2, 4, 8 and 10 s.
    for (const delay of [500, 1000, 2000, 4000, 8000, 10_000]) await vi.advanceTimersByTimeAsync(delay)
    expect(openSocket).toHaveBeenCalledTimes(7)
    // Attempts 8 and 9: still 10 s each, never longer.
    for (let n = 8; n <= 9; n++) {
      await vi.advanceTimersByTimeAsync(9_999)
      expect(openSocket).toHaveBeenCalledTimes(n - 1)
      await vi.advanceTimersByTimeAsync(1)
      expect(openSocket).toHaveBeenCalledTimes(n)
    }
  })

  it('marks the store unauthorized on a 401 and stops trying', async () => {
    const { store, api, openSocket } = rig()
    api.mockRejectedValue(new ApiError(401, 'unauthorized'))
    store.start()
    await flush()
    expect(store.state.unauthorized).toBe(true)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(openSocket).not.toHaveBeenCalled()
  })

  it('marks the store unauthorized when a reconnect gets a 401 (daemon restarted with a new token)', async () => {
    const { store, openSocket, sockets } = rig()
    store.start()
    await flush()
    openSocket.mockRejectedValue(new ApiError(401, 'unauthorized'))
    sockets[0].drop()
    await vi.advanceTimersByTimeAsync(500)
    expect(store.state.unauthorized).toBe(true)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(openSocket).toHaveBeenCalledTimes(2)
  })

  it('retries the first load when the daemon is not reachable', async () => {
    const { store, api, openSocket } = rig()
    api.mockRejectedValueOnce(new ApiError(0, 'down'))
    store.start()
    await flush()
    expect(store.state.connection).toBe('disconnected')
    expect(openSocket).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(500)
    expect(api).toHaveBeenCalledTimes(2)
    expect(openSocket).toHaveBeenCalledTimes(1)
  })

  it('refreshes the counts from the server after message events', async () => {
    const after = snapshot()
    after.workspaces[0].folders[0].pending = 1
    const { store, api, sockets } = rig([snapshot(), after])
    store.start()
    await flush()
    sockets[0].send({ type: 'state', ...snapshot() })
    const msg = { id: 'm1', workspace: 'w', from: 'b', to: 'a', body: 'hi', hop: 1, created: '2026-11-18T10:00:00Z', state: 'queued' }
    sockets[0].send({ type: 'message', ws: 'w', folder: 'a', message: msg })
    sockets[0].send({ type: 'message', ws: 'w', folder: 'a', message: { ...msg, state: 'nudged' } })
    await vi.advanceTimersByTimeAsync(200)
    expect(api).toHaveBeenCalledTimes(2) // the two events share one refresh
    expect(store.state.data!.workspaces[0].folders[0].pending).toBe(1)
  })

  it('keeps a status that arrived while a refresh was in flight', async () => {
    let release!: (s: State) => void
    const { store, api, sockets } = rig()
    store.start()
    await flush()
    sockets[0].send({ type: 'state', ...snapshot() })
    api.mockImplementationOnce(() => new Promise<State>((r) => (release = r)))
    const done = store.refresh()
    sockets[0].send({ type: 'status', ws: 'w', folder: 'a', status: 'busy' })
    release(snapshot()) // taken before the event: says "idle"
    await done
    expect(store.state.data!.workspaces[0].folders[0].status).toBe('busy')
  })

  it('drops a refresh answer when a new snapshot frame arrived meanwhile', async () => {
    let release!: (s: State) => void
    const { store, api, sockets } = rig()
    store.start()
    await flush()
    api.mockImplementationOnce(() => new Promise<State>((r) => (release = r)))
    const done = store.refresh()
    const s = snapshot()
    s.workspaces[0].folders[1].status = 'busy'
    sockets[0].send({ type: 'state', ...s })
    release(snapshot())
    await done
    expect(store.state.data!.workspaces[0].folders[1].status).toBe('busy')
  })

  it('passes every event to onEvent listeners', async () => {
    const { store, sockets } = rig()
    const seen: DaemonEvent[] = []
    const off = store.onEvent((e) => {
      seen[seen.length] = e
    })
    store.start()
    await flush()
    sockets[0].send({ type: 'state', ...snapshot() })
    sockets[0].send({ type: 'status', ws: 'w', folder: 'a', status: 'busy' })
    off()
    sockets[0].send({ type: 'status', ws: 'w', folder: 'a', status: 'idle' })
    expect(seen).toEqual([{ type: 'status', ws: 'w', folder: 'a', status: 'busy' }])
  })

  it('ignores a frame that is not valid JSON', async () => {
    const { store, sockets } = rig()
    store.start()
    await flush()
    sockets[0].send({ type: 'state', ...snapshot() })
    sockets[0].onmessage?.(new MessageEvent('message', { data: 'not json' }))
    expect(store.state.connection).toBe('connected')
    expect(store.state.data!.workspaces).toHaveLength(1)
  })

  it('stop closes the socket and cancels a pending reconnect', async () => {
    const { store, openSocket, sockets } = rig()
    store.start()
    await flush()
    sockets[0].drop()
    store.stop()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(openSocket).toHaveBeenCalledTimes(1)
  })
})
