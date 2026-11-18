import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { api, ApiError, openSocket } from '../api'
import { store } from '../stores/state'
import { panelStore } from '../stores/panels'
import TerminalPanel, { MIN_COLS, MIN_ROWS } from './TerminalPanel.vue'
import type { FolderState, Message } from '../types'

// Fakes for xterm.js and the fit addon. jsdom has no canvas or layout, so
// the real ones cannot measure anything here.
const fx = vi.hoisted(() => ({
  terms: [] as FakeTerm[],
  propose: { cols: 100, rows: 30 } as { cols: number; rows: number } | undefined,
  writeCbs: [] as (() => void)[],
}))

interface FakeTerm {
  cols: number
  rows: number
  options: Record<string, unknown>
  opened: HTMLElement | null
  write: ReturnType<typeof vi.fn>
  resize: ReturnType<typeof vi.fn>
  reset: ReturnType<typeof vi.fn>
  focus: ReturnType<typeof vi.fn>
  dispose: ReturnType<typeof vi.fn>
  dataCb: ((d: string) => void) | null
}

vi.mock('@xterm/xterm', () => {
  class Terminal {
    cols = 80
    rows = 24
    options: Record<string, unknown>
    opened: HTMLElement | null = null
    write = vi.fn((_d: unknown, cb?: () => void) => {
      if (cb) fx.writeCbs.push(cb)
    })
    resize = vi.fn((c: number, r: number) => {
      this.cols = c
      this.rows = r
    })
    reset = vi.fn()
    focus = vi.fn()
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

// The server's first frame is the replay; the panel treats it as done when
// xterm calls back after writing it.
async function replay(bytes = 'old screen'): Promise<void> {
  sock().frame(new TextEncoder().encode(bytes).buffer)
  fx.writeCbs.splice(0).forEach((cb) => cb())
}

function closeAll(): void {
  for (const p of [...panelStore.panels]) panelStore.closePanel(p.ws, p.folder)
}

beforeEach(() => {
  vi.useFakeTimers()
  fx.terms.length = 0
  fx.writeCbs = []
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
    setFolder({ status: 'idle' })
    await mountPanel()
    apiMock.mockImplementation(async (_m: string, path: string) => {
      if (path === '/api/agents/game-lounge/fe-admin/stop') throw new ApiError(409, 'agent has an unsent draft; clear it or use force')
      if (path.endsWith('/sessions')) return { sessions: [] }
      if (path.endsWith('/messages')) return { messages: [] }
      return undefined
    })
    setFolder({ status: 'idle' })
    await flushPromises()
    await w!.find('button[aria-label="Stop agent"]').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/stop')
    const alert = w!.find('[role="alert"]')
    expect(alert.text()).toContain('This agent has an unsent draft. Stop anyway discards it and ends the current turn.')
    await alert.find('button.force').trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/stop?force=true')
    expect(w!.find('[role="alert"]').exists()).toBe(false)
  })

  it('offers Stop anyway only for the draft refusal, not for another 409', async () => {
    setFolder({ status: 'idle' })
    await mountPanel()
    apiMock.mockImplementation(async (_m: string, path: string) => {
      if (path.endsWith('/stop')) throw new ApiError(409, 'folder is not running')
      if (path.endsWith('/sessions')) return { sessions: [] }
      return { messages: [] }
    })
    await w!.find('button[aria-label="Stop agent"]').trigger('click')
    await flushPromises()
    const alert = w!.find('[role="alert"]')
    expect(alert.text()).toContain('folder is not running')
    expect(alert.find('button.force').exists()).toBe(false)
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
    await chip.find('button').trigger('click') // the first click only asks
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/deliver-now')
  })

  it('warns before the click that the draft goes with the nudge, and asks before delivering', async () => {
    messages = [heldDraft('m1')]
    setFolder({ status: 'idle', pending: 1 })
    await mountPanel()
    const b = () => w!.find('.waiting button')
    expect(b().attributes('title')).toContain('draft')
    expect(b().attributes('title')).toContain('submitted together with the nudge')
    await b().trigger('click')
    expect(apiMock).not.toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/deliver-now')
    expect(b().text()).toBe('Submit draft too?')
    expect(b().attributes('aria-label')).toContain('submitted together with the nudge')
    // the ask goes away by itself
    vi.advanceTimersByTime(4100)
    await flushPromises()
    expect(b().text()).toBe('Deliver now')
    await b().trigger('click')
    await b().trigger('click')
    await flushPromises()
    expect(apiMock).toHaveBeenCalledWith('POST', '/api/agents/game-lounge/fe-admin/deliver-now')
    expect(apiMock.mock.calls.filter((c) => c[1].endsWith('/deliver-now'))).toHaveLength(1)
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
    expect(term().write).toHaveBeenCalledWith(new Uint8Array(bytes), expect.any(Function))
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
    await replay()
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

  it('sends the real size under the minimum, and keeps xterm at the size the PTY will have', async () => {
    fx.propose = { cols: 42, rows: 8 }
    await mountPanel()
    expect(sock().frames()).toEqual([{ type: 'resize', cols: 42, rows: 8 }])
    expect([term().cols, term().rows]).toEqual([MIN_COLS, MIN_ROWS])
    expect([MIN_COLS, MIN_ROWS]).toEqual([60, 10])
  })

  it('shows an edge fade while the terminal scrolls sideways, and hides it at the end', async () => {
    fx.propose = { cols: 42, rows: 12 }
    await mountPanel()
    const el = w!.find('.term').element as HTMLElement
    const set = (scrollWidth: number, clientWidth: number, scrollLeft: number) => {
      Object.defineProperty(el, 'scrollWidth', { value: scrollWidth, configurable: true })
      Object.defineProperty(el, 'clientWidth', { value: clientWidth, configurable: true })
      Object.defineProperty(el, 'scrollLeft', { value: scrollLeft, configurable: true })
    }
    expect(w!.find('.term-fade').exists()).toBe(false)
    set(432, 327, 0)
    for (const cb of roCallbacks) cb()
    vi.advanceTimersByTime(100)
    await flushPromises()
    expect(w!.find('.term-fade').exists()).toBe(true)
    set(432, 327, 105)
    el.dispatchEvent(new Event('scroll'))
    await flushPromises()
    expect(w!.find('.term-fade').exists()).toBe(false)
  })

  it('leaves xterm alone when the panel is bigger than the minimum', async () => {
    fx.propose = { cols: 100, rows: 30 }
    await mountPanel()
    expect([term().cols, term().rows]).toEqual([100, 30])
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

  it('clears xterm before the first write of each socket', async () => {
    await mountPanel()
    await replay()
    expect(term().reset.mock.invocationCallOrder[0]).toBeLessThan(term().write.mock.invocationCallOrder[0])
  })

  it('drops xterm automatic replies while the replay is written, then sends typing', async () => {
    await mountPanel()
    sock().frame(new TextEncoder().encode('old screen with a DA2 query').buffer)
    // xterm answers the query inside the replay before the write finishes
    term().dataCb!('\x1b[>0;276;0c')
    expect(sock().frames().filter((f) => f.type === 'input')).toEqual([])
    fx.writeCbs.splice(0).forEach((cb) => cb())
    term().dataCb!('x')
    expect(sock().frames().filter((f) => f.type === 'input')).toEqual([{ type: 'input', data: 'x' }])
  })

  it('a late replay write callback from an old socket does not open the gate of a new one', async () => {
    await mountPanel()
    sock().frame(new TextEncoder().encode('old screen').buffer)
    const late = fx.writeCbs.splice(0)
    sock().drop()
    await vi.advanceTimersByTimeAsync(500)
    expect(sockets).toHaveLength(2)
    sock().frame(new TextEncoder().encode('new screen').buffer)
    late.forEach((cb) => cb()) // the old socket's write finishes while the new replay is still parsing
    term().dataCb!('\x1b[>0;276;0c')
    expect(sock().frames().filter((f) => f.type === 'input')).toEqual([])
    fx.writeCbs.splice(0).forEach((cb) => cb())
    term().dataCb!('x')
    expect(sock().frames().filter((f) => f.type === 'input')).toEqual([{ type: 'input', data: 'x' }])
  })

  it('drops input before the replay frame arrives, and treats only the first frame as the replay', async () => {
    await mountPanel()
    term().dataCb!('early')
    await replay()
    sock().frame(new TextEncoder().encode('live').buffer)
    term().dataCb!('late') // live output is not a replay: its write has no gate
    expect(sock().frames().filter((f) => f.type === 'input')).toEqual([{ type: 'input', data: 'late' }])
    expect(term().write.mock.calls[1]).toHaveLength(1)
  })

  it('does not wait for a write callback when the replay is empty', async () => {
    await mountPanel()
    sock().frame(new ArrayBuffer(0))
    term().dataCb!('x')
    expect(sock().frames().filter((f) => f.type === 'input')).toEqual([{ type: 'input', data: 'x' }])
  })

  it('a new socket gates its own replay again', async () => {
    await mountPanel()
    await replay()
    sock().drop()
    await vi.advanceTimersByTimeAsync(500)
    expect(sockets).toHaveLength(2)
    term().dataCb!('x')
    expect(sock().frames().filter((f) => f.type === 'input')).toEqual([])
  })

  it('does not reset the backoff on the replay frame, only on a later frame', async () => {
    await mountPanel()
    await replay()
    sock().drop() // waits 500, next wait is 1000
    await vi.advanceTimersByTimeAsync(500)
    expect(sockets).toHaveLength(2)
    await replay()
    sock().drop() // the replay did not reset: waits 1000
    await vi.advanceTimersByTimeAsync(999)
    expect(sockets).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(sockets).toHaveLength(3)
    await replay()
    sock().frame(new TextEncoder().encode('live').buffer) // a frame after the replay resets it
    sock().drop()
    await vi.advanceTimersByTimeAsync(500)
    expect(sockets).toHaveLength(4)
  })

  it('resets the backoff after the socket stayed open for 3 s', async () => {
    await mountPanel()
    await replay()
    sock().drop()
    await vi.advanceTimersByTimeAsync(500)
    await replay()
    await vi.advanceTimersByTimeAsync(3000)
    sock().drop()
    await vi.advanceTimersByTimeAsync(500)
    expect(sockets).toHaveLength(3)
  })

  it('does not reset the backoff when the socket closes before 3 s', async () => {
    await mountPanel()
    await replay()
    sock().drop()
    await vi.advanceTimersByTimeAsync(500)
    await replay()
    await vi.advanceTimersByTimeAsync(2999)
    sock().drop()
    await vi.advanceTimersByTimeAsync(999)
    expect(sockets).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(sockets).toHaveLength(3)
  })

  it('caps the backoff at 10 s', async () => {
    await mountPanel()
    sock().drop()
    openMock.mockRejectedValue(new ApiError(0, 'could not open'))
    // 500 (socket 2 attempt happens at once after the first wait), then every failure waits longer
    const waits = [500, 1000, 2000, 4000, 8000, 10000, 10000]
    let calls = openMock.mock.calls.length
    for (const wait of waits) {
      await vi.advanceTimersByTimeAsync(wait - 1)
      expect(openMock.mock.calls.length).toBe(calls)
      await vi.advanceTimersByTimeAsync(1)
      calls++
      expect(openMock.mock.calls.length).toBe(calls)
    }
  })

  describe('OSC 8 links', () => {
    async function activate(url: string): Promise<void> {
      const lh = (term().options as { linkHandler: { activate(e: MouseEvent, t: string): void } }).linkHandler
      lh.activate(new MouseEvent('click'), url)
      await flushPromises()
    }
    const ask = () => w!.find('.link-ask')

    it('shows the real URL and waits for Open before opening anything', async () => {
      await mountPanel()
      const open = vi.fn()
      vi.stubGlobal('open', open)
      await activate('https://example.com/a?x=1')
      expect(open).not.toHaveBeenCalled()
      expect(ask().exists()).toBe(true)
      expect(ask().text()).toContain('Open https://example.com/a?x=1?')
      expect(ask().findAll('button').map((b) => b.text())).toEqual(['Open', 'Cancel'])
      await ask().findAll('button')[0].trigger('click')
      expect(open.mock.calls).toEqual([['https://example.com/a?x=1', '_blank', 'noopener,noreferrer']])
      expect(ask().exists()).toBe(false)
    })

    it('Cancel and Escape close the ask without opening', async () => {
      await mountPanel()
      const open = vi.fn()
      vi.stubGlobal('open', open)
      await activate('http://127.0.0.1:3000/')
      await ask().findAll('button')[1].trigger('click')
      expect(ask().exists()).toBe(false)
      await activate('http://127.0.0.1:3000/')
      await ask().trigger('keydown', { key: 'Escape' })
      expect(ask().exists()).toBe(false)
      expect(open).not.toHaveBeenCalled()
    })

    it('never asks for, or opens, anything but http and https', async () => {
      await mountPanel()
      const open = vi.fn()
      vi.stubGlobal('open', open)
      for (const u of ['javascript:alert(1)', 'file:///etc/passwd', 'data:text/html,x', 'not a url']) {
        await activate(u)
        expect(ask().exists()).toBe(false)
      }
      expect(open).not.toHaveBeenCalled()
    })

    it('a newer link replaces the ask, and the ask shows the normalized URL it will open', async () => {
      await mountPanel()
      await activate('https://a.example/')
      await activate('HTTPS://B.example:443/x y')
      expect(w!.findAll('.link-ask')).toHaveLength(1)
      expect(ask().text()).toContain('Open https://b.example/x%20y?')
    })
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

describe('TerminalPanel header layout', () => {
  function paneWidth(px: number) {
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const width = this.classList.contains('pane') ? px : 0
      return { width, height: 0, top: 0, left: 0, right: width, bottom: 0, x: 0, y: 0, toJSON() {} }
    })
  }
  afterEach(() => vi.restoreAllMocks())

  it('keeps the waiting chip on row 1 when the panel is wide, and never adds a third row', async () => {
    messages = [heldDraft('m1')]
    setFolder({ status: 'idle', pending: 1 })
    paneWidth(900)
    await mountPanel()
    expect(w!.find('.ph-1 .waiting').exists()).toBe(true)
    expect(w!.find('.ph-2 .waiting').exists()).toBe(false)
    expect(w!.find('.ph').element.children).toHaveLength(2)
  })

  it('moves the waiting chip to the end of row 2 when the panel is narrow', async () => {
    messages = [heldDraft('m1')]
    setFolder({ status: 'idle', pending: 1 })
    paneWidth(500)
    await mountPanel()
    expect(w!.find('.ph-1 .waiting').exists()).toBe(false)
    const row2 = w!.find('.ph-2').element
    expect(row2.lastElementChild?.classList.contains('waiting')).toBe(true)
    expect(w!.find('.ph').element.children).toHaveLength(2)
  })

  it('lets row 2 wrap only when the chip sits in it, so the button is never pushed out', async () => {
    messages = [heldDraft('m1')]
    setFolder({ status: 'idle', pending: 1 })
    paneWidth(500)
    await mountPanel()
    expect(w!.find('.ph-2').classes()).toContain('wrap')
    vi.restoreAllMocks()
    paneWidth(900)
    for (const cb of roCallbacks) cb()
    await flushPromises()
    expect(w!.find('.ph-2').classes()).not.toContain('wrap')
  })

  it('goes compact in a very narrow panel, so row 2 does not overflow', async () => {
    messages = [heldDraft('m1')]
    setFolder({ status: 'idle', pending: 1 })
    paneWidth(380)
    await mountPanel()
    expect(w!.find('.pane').classes()).toContain('tight')
    vi.restoreAllMocks()
    paneWidth(500)
    for (const cb of roCallbacks) cb()
    await flushPromises()
    expect(w!.find('.pane').classes()).not.toContain('tight')
  })

  it('the compact chip is still read as "1 message waiting"', async () => {
    messages = [heldDraft('m1')]
    setFolder({ status: 'idle', pending: 1 })
    paneWidth(380)
    await mountPanel()
    expect(w!.find('.pane').classes()).toContain('tight')
    expect(w!.find('.waiting .chip').text()).toBe('1 message waiting')
    expect(w!.find('.waiting .chip .opt').exists()).toBe(true)
  })

  it('follows the panel when it is resized', async () => {
    messages = [heldDraft('m1')]
    setFolder({ status: 'idle', pending: 1 })
    paneWidth(900)
    await mountPanel()
    vi.restoreAllMocks()
    paneWidth(500)
    for (const cb of roCallbacks) cb()
    await flushPromises()
    expect(w!.find('.ph-2 .waiting').exists()).toBe(true)
  })

  it('Deliver now uses the same button as the Messages drawer, with its icon', async () => {
    messages = [heldDraft('m1')]
    setFolder({ status: 'idle', pending: 1 })
    await mountPanel()
    const b = w!.find('.waiting button')
    expect(b.classes()).toEqual(expect.arrayContaining(['btn', 'act']))
    expect(b.find('svg').exists()).toBe(true)
    expect(b.text()).toBe('Deliver now')
  })

  it('Restore has its own icon, not the Maximize one', async () => {
    await mountPanel()
    const maxSvg = w!.find('button[aria-label="Maximize"] svg').html()
    await w!.setProps({ maximized: true })
    const restoreSvg = w!.find('button[aria-label="Restore layout"] svg').html()
    expect(restoreSvg).not.toBe(maxSvg)
  })

  it('shows a short note when the messages could not be loaded', async () => {
    apiMock.mockImplementation(async (m: string, path: string) => {
      if (path.endsWith('/sessions')) return { sessions: [] }
      if (path.endsWith('/messages')) throw new ApiError(500, 'could not load the messages')
      return m === 'POST' ? undefined : undefined
    })
    await mountPanel()
    const note = w!.find('.p-note')
    expect(note.exists()).toBe(true)
    expect(note.text()).toContain('Held messages could not be loaded')
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

describe('TerminalPanel final review fixes', () => {
  const stopBtn = () => w!.find('button.stop-btn')
  const draftRefusal = () =>
    apiMock.mockImplementation(async (_m: string, path: string) => {
      if (path.endsWith('/stop') && !path.includes('force')) throw new ApiError(409, 'agent has an unsent draft; clear it or use force')
      if (path.endsWith('/sessions')) return { sessions: [] }
      if (path.endsWith('/messages')) return { messages: [] }
      return undefined
    })
  const stopCalls = () => apiMock.mock.calls.filter((c) => String(c[1]).includes('/stop'))

  it('clears a stale Stop anyway when the status changes, so it cannot force-kill a newer agent', async () => {
    setFolder({ status: 'idle' })
    await mountPanel()
    draftRefusal()
    await stopBtn().trigger('click')
    await flushPromises()
    expect(w!.find('button.force').exists()).toBe(true)
    setFolder({ status: 'busy' })
    await flushPromises()
    expect(w!.find('[role="alert"]').exists()).toBe(false)
    expect(w!.find('button.force').exists()).toBe(false)
  })

  it('clears a stale Stop anyway when the agent stops and a new session starts', async () => {
    setFolder({ status: 'idle' })
    await mountPanel()
    draftRefusal()
    await stopBtn().trigger('click')
    await flushPromises()
    setFolder({ status: 'idle', session_id: 'aaaaaaaa-0000-4000-8000-000000000002' })
    await flushPromises()
    expect(w!.find('button.force').exists()).toBe(false)
    expect(w!.find('[role="alert"]').exists()).toBe(false)
  })

  it('stopped agent clears the alert too', async () => {
    setFolder({ status: 'idle' })
    await mountPanel()
    draftRefusal()
    await stopBtn().trigger('click')
    await flushPromises()
    setFolder({ status: 'stopped' })
    await flushPromises()
    expect(w!.find('[role="alert"]').exists()).toBe(false)
  })

  it('busy agent: first click only asks, second click stops', async () => {
    await mountPanel()
    expect(stopBtn().text()).toBe('')
    await stopBtn().trigger('click')
    expect(stopCalls()).toHaveLength(0)
    expect(stopBtn().text()).toBe('Stop? It is working')
    expect(stopBtn().classes()).toContain('confirm')
    expect(stopBtn().attributes('aria-label')).toBe('Stop? It is working')
    await stopBtn().trigger('click')
    await flushPromises()
    expect(stopCalls()).toEqual([['POST', '/api/agents/game-lounge/fe-admin/stop']])
    expect(stopBtn().text()).toBe('')
  })

  it('busy confirm resets by itself after 4 s', async () => {
    await mountPanel()
    await stopBtn().trigger('click')
    await vi.advanceTimersByTimeAsync(3900)
    expect(stopBtn().classes()).toContain('confirm')
    await vi.advanceTimersByTimeAsync(200)
    expect(stopBtn().classes()).not.toContain('confirm')
    await stopBtn().trigger('click')
    expect(stopCalls()).toHaveLength(0)
  })

  it('busy confirm resets when the status changes', async () => {
    await mountPanel()
    await stopBtn().trigger('click')
    setFolder({ status: 'idle' })
    await flushPromises()
    expect(stopBtn().classes()).not.toContain('confirm')
  })

  it('idle agent stops on the first click', async () => {
    setFolder({ status: 'idle' })
    await mountPanel()
    await stopBtn().trigger('click')
    await flushPromises()
    expect(stopCalls()).toHaveLength(1)
  })

  it('focuses xterm after attach when the panel is focused', async () => {
    await mountPanel({ focused: true })
    await flushPromises()
    expect(term().focus).toHaveBeenCalled()
  })

  it('does not focus xterm after attach when the panel is not focused', async () => {
    await mountPanel({ focused: false })
    await flushPromises()
    expect(term().focus).not.toHaveBeenCalled()
  })

  it('focuses xterm when the panel becomes focused (sidebar click)', async () => {
    await mountPanel({ focused: false })
    await flushPromises()
    await w!.setProps({ focused: true })
    await flushPromises()
    expect(term().focus).toHaveBeenCalledTimes(1)
  })

  it('does not pull focus from a header button of the same panel', async () => {
    await mountPanel({ focused: false })
    await flushPromises()
    const b = w!.find('button.stop-btn').element as HTMLButtonElement
    b.focus()
    await w!.setProps({ focused: true })
    await flushPromises()
    expect(term().focus).not.toHaveBeenCalled()
  })
})
