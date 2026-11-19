// The daemon state the whole UI reads. It loads GET /api/state, then follows
// /ws/events: the first frame there is a full snapshot, every later frame is
// one change. When the socket drops it reconnects with backoff and the next
// snapshot replaces everything, so no missed event can leave stale data.
//
// Components read store.state and store.ui (reactive) and never write
// store.state themselves. Tasks 16 to 18 call store.refresh() after a change
// that has no event (adding a workspace or folder), and store.onEvent() for
// the raw event stream (for example the Messages timeline).
import { reactive } from 'vue'
import { api, ApiError, openSocket } from '../api'
import type { Account, DaemonEvent, EventFrame, State, StatusEvent, WorkspaceState } from '../types'

export type Connection = 'connecting' | 'connected' | 'disconnected'

export interface StoreState {
  connection: Connection
  // The token is missing or the daemon refused it (it changes on every
  // start). The app then shows only the "open the link" screen.
  unauthorized: boolean
  data: State | null
}

export interface UiState {
  workspace: string | null // selected workspace; null means the first one
  messagesOpen: boolean // the Messages drawer
}

export interface StoreDeps {
  api: typeof api
  openSocket: typeof openSocket
}

// Reconnect delays in ms; the last one repeats.
const backoff = [500, 1000, 2000, 4000, 8000, 10000]
// Message events change the unread counts, which no event carries. A burst
// of them shares one GET /api/state after this delay.
const refreshDelay = 150

// A GET /api/state in flight. Events that arrive meanwhile are kept, so an
// answer taken before them cannot undo them.
interface Inflight {
  seq: number
  statuses: Map<string, StatusEvent>
  account?: Account
  sawMessage: boolean
  stale: boolean // a newer snapshot was applied; drop this answer
}

export function createStore(deps: StoreDeps = { api, openSocket }) {
  const state = reactive<StoreState>({ connection: 'connecting', unauthorized: false, data: null })
  const ui = reactive<UiState>({ workspace: null, messagesOpen: false })
  const listeners = new Set<(e: DaemonEvent) => void>()

  let running = false
  let socket: WebSocket | null = null
  let attempt = 0
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let refreshTimer: ReturnType<typeof setTimeout> | undefined
  let seq = 0
  let inflight: Inflight[] = []

  function start(): void {
    if (running) return
    running = true
    void connect()
  }

  function stop(): void {
    running = false
    clearTimeout(retryTimer)
    clearTimeout(refreshTimer)
    retryTimer = refreshTimer = undefined
    const s = socket
    socket = null
    s?.close()
  }

  async function connect(): Promise<void> {
    retryTimer = undefined
    if (!running) return
    try {
      if (state.data === null) state.data = await deps.api<State>('GET', '/api/state')
      const ws = await deps.openSocket('/ws/events')
      if (!running) {
        ws.close()
        return
      }
      attach(ws)
    } catch (e) {
      fail(e)
    }
  }

  function fail(e: unknown): void {
    if (e instanceof ApiError && e.status === 401) {
      state.unauthorized = true
      stop()
      return
    }
    state.connection = 'disconnected'
    scheduleRetry()
  }

  function scheduleRetry(): void {
    if (!running || retryTimer !== undefined) return
    const delay = backoff[Math.min(attempt, backoff.length - 1)]
    attempt++
    retryTimer = setTimeout(() => void connect(), delay)
  }

  function attach(ws: WebSocket): void {
    socket = ws
    ws.onerror = null
    ws.onmessage = (ev: MessageEvent) => {
      if (socket === ws) onFrame(ev.data)
    }
    ws.onclose = () => {
      if (socket !== ws) return
      socket = null
      if (!running) return
      state.connection = 'disconnected'
      scheduleRetry()
    }
  }

  function onFrame(raw: unknown): void {
    let f: EventFrame
    try {
      f = JSON.parse(String(raw)) as EventFrame
    } catch {
      return
    }
    if (f === null || typeof f !== 'object') return
    switch (f.type) {
      case 'state': {
        const { type, ...snap } = f
        state.data = snap
        for (const r of inflight) r.stale = true
        state.connection = 'connected'
        attempt = 0
        return
      }
      case 'status':
        for (const r of inflight) r.statuses.set(`${f.ws}\n${f.folder}`, f)
        if (state.data && !applyStatus(state.data, f)) scheduleRefresh() // a folder added since the snapshot
        break
      case 'account':
        for (const r of inflight) r.account = f.account
        if (state.data) state.data.account = f.account
        break
      case 'message':
        for (const r of inflight) r.sawMessage = true
        scheduleRefresh()
        break
      default:
        return
    }
    for (const fn of listeners) {
      try {
        fn(f)
      } catch (err) {
        console.error('relaydeck: event listener failed', err)
      }
    }
  }

  function scheduleRefresh(): void {
    if (refreshTimer !== undefined) return
    refreshTimer = setTimeout(() => {
      refreshTimer = undefined
      void refresh()
    }, refreshDelay)
  }

  // refresh reloads GET /api/state. Errors other than 401 are ignored: the
  // event socket reconnects and its snapshot brings the state back.
  async function refresh(): Promise<void> {
    const mine: Inflight = { seq: ++seq, statuses: new Map(), sawMessage: false, stale: false }
    inflight = [...inflight, mine]
    try {
      const snap = await deps.api<State>('GET', '/api/state')
      if (mine.stale) return
      for (const ev of mine.statuses.values()) applyStatus(snap, ev)
      if (mine.account) snap.account = mine.account
      state.data = snap
      for (const r of inflight) if (r.seq < mine.seq) r.stale = true
      if (mine.sawMessage) scheduleRefresh()
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) fail(e)
    } finally {
      inflight = inflight.filter((r) => r !== mine)
    }
  }

  function onEvent(fn: (e: DaemonEvent) => void): () => void {
    listeners.add(fn)
    return () => listeners.delete(fn)
  }

  function activeWorkspace(): WorkspaceState | null {
    const wss = state.data?.workspaces ?? []
    return wss.find((w) => w.name === ui.workspace) ?? wss[0] ?? null
  }

  return { state, ui, start, stop, refresh, onEvent, activeWorkspace }
}

// applyStatus sets one folder's status and reports whether the folder exists.
function applyStatus(s: State, ev: StatusEvent): boolean {
  const f = s.workspaces.find((w) => w.name === ev.ws)?.folders.find((x) => x.name === ev.folder)
  if (!f) return false
  f.status = ev.status
  if (ev.status === 'stopped') delete f.screen
  return true
}

export type Store = ReturnType<typeof createStore>

export const store = createStore()
