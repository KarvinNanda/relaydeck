import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError, openSocket } from '../api'
import { store } from '../stores/state'
import { panelStore } from '../stores/panels'
import TerminalPanel from './TerminalPanel.vue'
import type { FolderState, Message } from '../types'

// Fakes for xterm.js and the fit addon. jsdom has no canvas or layout, so
// the real ones cannot measure anything here.
const fx = vi.hoisted(() => ({
  terms: [] as FakeTerm[],
  propose: { cols: 100, rows: 30 } as { cols: number; rows: number } | undefined,
}))

interface FakeTerm {
  cols: number
  rows: number
  options: Record<string, unknown>
  opened: HTMLElement | null
  write: ReturnType<typeof vi.fn>
  reset: ReturnType<typeof vi.fn>
  dispose: ReturnType<typeof vi.fn>
  dataCb: ((d: string) => void) | null
}

vi.mock('@xterm/xterm', () => {
  class Terminal {
    cols = 80
    rows = 24
    options: Record<string, unknown>
    opened: HTMLElement | null = null
    write = vi.fn()
    reset = vi.fn()
    dispose = vi.fn()
    dataCb: ((d: string) => void) | null = null
    constructor(opts: Record<string, unknown>) {
      this.options = opts
      fx.terms.push(this as unknown as FakeTerm)
    }
    open(el: HTMLElement) {
      this.opened = el
    }
    onData(cb: (d: string) => void) {
      this.dataCb = cb
      return { dispose: () => (this.dataCb = null) }
    }
    loadAddon(a: { activate(t: unknown): void }) {
      a.activate(this)
    }
  }
  return { Terminal }
})

vi.mock('@xterm/addon-fit', () => {
  class FitAddon {
    term: FakeTerm | null = null
    activate(t: FakeTerm) {
      this.term = t
    }
    dispose() {}
    proposeDimensions() {
      return fx.propose
    }
    fit() {
      if (!fx.propose || !this.term) return
      this.term.cols = fx.propose.cols
      this.term.rows = fx.propose.rows
    }
  }
  return { FitAddon }
})

vi.mock('../api', async (importOriginal) => {
  const real = await importOriginal<typeof import('../api')>()
  return { ...real, api: vi.fn(), openSocket: vi.fn() }
})
const apiMock = vi.mocked(api)
const openMock = vi.mocked(openSocket)

// A fake socket that the panel drives like a WebSocket.
class FakeSocket {
  readyState = 1
  binaryType = 'blob'
  sent: string[] = []
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  closed = false
  send(s: string) {
    this.sent.push(s)
  }
  close() {
    this.closed = true
    this.readyState = 3
  }
  // the server side
  frame(data: unknown) {
    this.onmessage?.({ data })
  }
  drop() {
    this.readyState = 3
    this.onclose?.()
  }
  frames() {
    return this.sent.map((s) => JSON.parse(s))
  }
}

// ResizeObserver stub: the test fires the callback by hand.
let roCallbacks: (() => void)[] = []
class FakeRO {
  cb: () => void
  constructor(cb: () => void) {
    this.cb = cb
    roCallbacks.push(cb)
  }
  observe() {}
  disconnect() {
    roCallbacks = roCallbacks.filter((c) => c !== this.cb)
  }
}

const sid = '7f3a9c10-0000-4000-8000-000000000001'
let sockets: FakeSocket[] = []
let messages: Message[] = []
let w: VueWrapper | null = null

function folder(over: Partial<FolderState> = {}): FolderState {
  return {
    name: 'fe-admin',
    path: '/x/fe-admin',
    account: 'work-2',
    permission_mode: 'auto',
    session_id: sid,
    status: 'busy',
    on_default_login: false,
    pending: 0,
    awaiting_approval: 0,
    ...over,
  }
}

function setFolder(over: Partial<FolderState> = {}, platform = 'darwin'): void {
  store.state.data = {
    platform,
    account: { cswap: true, known: true, default: '1' },
    workspaces: [{ name: 'game-lounge', folders: [folder(over)] }],
  }
}

function heldDraft(id: string): Message {
  return { id, workspace: 'game-lounge', from: 'be', to: 'fe-admin', body: 'API is up', hop: 1, created: '2026-11-18T10:42:05Z', state: 'held_draft' }
}

async function mountPanel(props: Partial<{ focused: boolean; maximized: boolean; canMaximize: boolean }> = {}) {
  w = mount(TerminalPanel, {
    props: { ws: 'game-lounge', folder: 'fe-admin', focused: false, maximized: false, canMaximize: true, ...props },
    attachTo: document.body,
  })
  await flushPromises()
  return w
}

function term(): FakeTerm {
  return fx.terms[fx.terms.length - 1]
}

function sock(): FakeSocket {
  return sockets[sockets.length - 1]
}

function closeAll(): void {
  for (const p of [...panelStore.panels]) panelStore.closePanel(p.ws, p.folder)
}

beforeEach(() => {
  vi.useFakeTimers()
  fx.terms.length = 0
  fx.propose = { cols: 100, rows: 30 }
  sockets = []
  messages = []
  roCallbacks = []
  vi.stubGlobal('ResizeObserver', FakeRO)
  closeAll()
  setFolder()
  openMock.mockReset()
  openMock.mockImplementation(async () => {
    const s = new FakeSocket()
    sockets.push(s)
    return s as unknown as WebSocket
  })
  apiMock.mockReset()
  apiMock.mockImplementation(async (method: string, path: string) => {
    if (method === 'GET' && path === '/api/workspaces/game-lounge/folders/fe-admin/sessions') {
      return { sessions: [{ id: sid, modified: new Date(Date.now() - 2 * 60000).toISOString(), title: 'Fix login redirect loop' }] }
    }
    if (method === 'GET' && path === '/api/workspaces/game-lounge/messages') return { messages }
    if (method === 'POST') return undefined
    throw new Error(`unexpected ${method} ${path}`)
  })
})

afterEach(() => {
  w?.unmount()
  w = null
  vi.useRealTimers()
  vi.unstubAllGlobals()
  store.state.data = null
  closeAll()
  document.body.innerHTML = ''
})

describe('TerminalPanel header', () => {
  it('shows folder, status, account and session title', async () => {
    await mountPanel()
    const h = w!.find('.ph')
    expect(h.find('.ph-name').text()).toBe('fe-admin')
    expect(h.find('.badge').text()).toBe('busy')
    expect(h.find('.ph-2').text()).toContain('work-2')
    expect(h.find('.ph-2').text()).toContain('Fix login redirect loop')
    expect(h.find('.ph-2').text()).not.toContain('on default login')
  })

  it('shows no account, the default login warning, and the short id when there is no title', async () => {
    apiMock.mockImplementation(async (_m: string, path: string) => {
      if (path.endsWith('/sessions')) return { sessions: [{ id: sid, modified: '2026-11-18T10:00:00Z', title: '' }] }
      if (path.endsWith('/messages')) return { messages: [] }
      return undefined
    })
    setFolder({ account: undefined, on_default_login: true })
    await mountPanel()
    const row = w!.find('.ph-2').text()
    expect(row).toContain('no account')
    expect(row).toContain('on default login')
    expect(row).toContain(sid.slice(0, 8))
  })

  it('hides the account and the warning on windows', async () => {
    setFolder({ account: undefined, on_default_login: true }, 'windows')
    await mountPanel()
    const row = w!.find('.ph-2').text()
    expect(row).not.toContain('no account')
    expect(row).not.toContain('on default login')
  })

  it('stop calls the stop route; a draft refusal offers Stop anyway with force', async () => {
    await mountPanel()
    apiMock.mockImplementation(async (_m: string, path: string) => {
      if (path === '/api/agents/game-lounge/fe-admin/stop') throw new ApiError(409, 'agent has an unsent draft; clear it or use force')
      if (path.endsWith('/sessions')) return { sessions: [] }
      if (path.endsWith('/messages')) return { messages: [] }
      return undefined
    })
    await w!.find('button[aria-label="Stop agent"]').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/stop')
    const alert = w!.find('[role="alert"]')
    expect(alert.text()).toContain('agent has an unsent draft')
    await alert.find('button.force').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/stop?force=true')
    expect(w!.find('[role="alert"]').exists()).toBe(false)
  })

  it('stop is disabled when the agent is stopped', async () => {
    setFolder({ status: 'stopped' })
    await mountPanel()
    expect(w!.find('button[aria-label="Stop agent"]').attributes('disabled')).toBeDefined()
  })

  it('close only closes the view and says so', async () => {
    panelStore.openPanel('game-lounge', 'fe-admin')
    await mountPanel()
    const b = w!.find('button[aria-label="Close panel"]')
    expect(b.attributes('title')).toContain('agent keeps running')
    await b.trigger('click')
    expect(panelStore.panels).toHaveLength(0)
    expect(apiMock).not.toHaveBeenCalledWith('POST', expect.stringContaining('/stop'))
  })

  it('maximize and restore emit events; maximize is off with one panel', async () => {
    await mountPanel()
    await w!.find('button[aria-label="Maximize"]').trigger('click')
    expect(w!.emitted('maximize')).toHaveLength(1)
    await w!.setProps({ maximized: true })
    await w!.find('button[aria-label="Restore layout"]').trigger('click')
    expect(w!.emitted('restore')).toHaveLength(1)
    await w!.setProps({ maximized: false, canMaximize: false })
    expect(w!.find('button[aria-label="Maximize"]').attributes('disabled')).toBeDefined()
  })

  it('opens the session picker as a popover from the Sessions button', async () => {
    await mountPanel()
    const b = w!.find('button.sessions-btn')
    expect(b.attributes('aria-expanded')).toBe('false')
    await b.trigger('click')
    await flushPromises()
    expect(b.attributes('aria-expanded')).toBe('true')
    expect(w!.find('.pop-wrap [role="dialog"][aria-label="Sessions"]').exists()).toBe(true)
    await w!.find('button[aria-label="Close sessions"]').trigger('click')
    expect(w!.find('.pop-wrap').exists()).toBe(false)
  })
})

describe('TerminalPanel waiting messages', () => {
  it('shows N messages waiting with Deliver now when idle with a draft', async () => {
    messages = [heldDraft('m1'), heldDraft('m2'), { ...heldDraft('m3'), to: 'be' }]
    setFolder({ status: 'idle', pending: 2 })
    await mountPanel()
    const chip = w!.find('.waiting')
    expect(chip.text()).toContain('2 messages waiting')
    await chip.find('button').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/deliver-now')
  })

  it('says 1 message and hides the chip without a draft hold or when busy', async () => {
    messages = [heldDraft('m1')]
    setFolder({ status: 'idle', pending: 1 })
    await mountPanel()
    expect(w!.find('.waiting').text()).toContain('1 message waiting')
    setFolder({ status: 'busy', pending: 1 })
    await flushPromises()
    expect(w!.find('.waiting').exists()).toBe(false)
    messages = []
    w!.unmount()
    setFolder({ status: 'idle', pending: 1 }) // a nudged message, not held by a draft
    await mountPanel()
    expect(w!.find('.waiting').exists()).toBe(false)
  })
})

describe('TerminalPanel terminal', () => {
  it('opens /ws/term for the folder, clears xterm and writes binary output', async () => {
    await mountPanel()
    expect(openMock).toHaveBeenCalledWith('/ws/term/game-lounge/fe-admin')
    expect(term().opened).toBe(w!.find('.term').element)
    expect(sock().binaryType).toBe('arraybuffer')
    expect(term().reset).toHaveBeenCalledTimes(1)
    const bytes = new TextEncoder().encode('hello')
    sock().frame(bytes.buffer)
    expect(term().write).toHaveBeenCalledWith(new Uint8Array(bytes))
  })

  it('themes xterm from the tokens', async () => {
    document.documentElement.style.setProperty('--term', '#0B1222')
    document.documentElement.style.setProperty('--fs-term', '12px')
    await mountPanel()
    const o = term().options as { fontSize: number; theme: { background: string }; fontFamily: string }
    expect(o.fontSize).toBe(12)
    expect(o.theme.background).toBe('#0B1222')
    document.documentElement.removeAttribute('style')
  })

  it('sends xterm input as {"type":"input"} text frames', async () => {
    await mountPanel()
    term().dataCb!('ls -la\r')
    expect(sock().frames()).toContainEqual({ type: 'input', data: 'ls -la\r' })
  })

  it('sends a resize frame after the fit on connect', async () => {
    await mountPanel()
    expect(sock().frames()).toEqual([{ type: 'resize', cols: 100, rows: 30 }])
  })

  it('fits and sends resize after the panel resizes, debounced about 100 ms', async () => {
    await mountPanel()
    sock().sent = []
    fx.propose = { cols: 120, rows: 40 }
    for (const cb of roCallbacks) cb()
    for (const cb of roCallbacks) cb()
    vi.advanceTimersByTime(99)
    expect(sock().sent).toHaveLength(0)
    vi.advanceTimersByTime(1)
    expect(sock().frames()).toEqual([{ type: 'resize', cols: 120, rows: 40 }])
  })

  it('sends the real size under 60 columns; the server clamps it', async () => {
    fx.propose = { cols: 42, rows: 12 }
    await mountPanel()
    expect(sock().frames()).toEqual([{ type: 'resize', cols: 42, rows: 12 }])
  })

  it('sends no resize when the panel has no size', async () => {
    fx.propose = undefined
    await mountPanel()
    expect(sock().sent).toHaveLength(0)
  })

  it('reconnects with backoff after a close and starts from a clean xterm', async () => {
    await mountPanel()
    expect(sockets).toHaveLength(1)
    sock().drop()
    await vi.advanceTimersByTimeAsync(499)
    expect(sockets).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(sockets).toHaveLength(2)
    expect(term().reset).toHaveBeenCalledTimes(2)
    // a failed reconnect waits longer
    openMock.mockRejectedValueOnce(new ApiError(0, 'could not open'))
    sock().drop()
    await vi.advanceTimersByTimeAsync(1000)
    expect(openMock).toHaveBeenCalledTimes(3)
    expect(sockets).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1999)
    expect(sockets).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(sockets).toHaveLength(3)
  })

  it('stops reconnecting and closes the socket on unmount', async () => {
    await mountPanel()
    const s = sock()
    w!.unmount()
    w = null
    expect(s.closed).toBe(true)
    expect(term().dispose).toHaveBeenCalled()
    s.drop()
    await vi.advanceTimersByTimeAsync(20000)
    expect(sockets).toHaveLength(1)
  })
})

describe('TerminalPanel stopped state', () => {
  it('shows Stopped with the last session and no terminal socket', async () => {
    setFolder({ status: 'stopped', pending: 1 })
    await mountPanel()
    expect(openMock).not.toHaveBeenCalled()
    const st = w!.find('.stopped')
    expect(st.text()).toContain('Stopped')
    expect(st.text()).toContain('Last session: Fix login redirect loop · 2 min ago · 1 message waiting')
    expect((w!.find('.term').element as HTMLElement).style.display).toBe('none')
  })

  it('Resume session and New session call start', async () => {
    setFolder({ status: 'stopped' })
    await mountPanel()
    await w!.find('button.resume').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/start', { session_id: sid })
    await w!.find('button.new').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/start', { new: true })
  })

  it('offers only New session when there is no session yet', async () => {
    setFolder({ status: 'stopped', session_id: undefined })
    await mountPanel()
    expect(w!.find('.stopped').text()).toContain('No session yet')
    expect(w!.find('button.resume').exists()).toBe(false)
    expect(w!.find('button.new').exists()).toBe(true)
  })

  it('shows a start error in the panel alert', async () => {
    setFolder({ status: 'stopped' })
    await mountPanel()
    apiMock.mockImplementation(async (_m: string, path: string) => {
      if (path.endsWith('/start')) throw new ApiError(409, 'session is open in another terminal')
      return { sessions: [], messages: [] }
    })
    await w!.find('button.resume').trigger('click')
    await flushPromises()
    expect(w!.find('[role="alert"]').text()).toContain('session is open in another terminal')
  })

  it('connects when the agent runs again and disconnects when it stops', async () => {
    setFolder({ status: 'stopped' })
    await mountPanel()
    setFolder({ status: 'starting' })
    await flushPromises()
    expect(openMock).toHaveBeenCalledTimes(1)
    expect((w!.find('.term').element as HTMLElement).style.display).toBe('')
    const s = sock()
    setFolder({ status: 'stopped' })
    await flushPromises()
    expect(s.closed).toBe(true)
    s.drop()
    await vi.advanceTimersByTimeAsync(20000)
    expect(openMock).toHaveBeenCalledTimes(1)
  })
})
