<script lang="ts">
// The smallest terminal the server gives a PTY: internal/server/ws.go clamps
// every resize to minCols and minRows. Keep these two in step with it. The
// panel still sends the size it really fits, then sizes xterm to at least
// this, so xterm draws the same grid the PTY has.
export const MIN_COLS = 60
export const MIN_ROWS = 10

// The text of daemon.ErrDraft (internal/daemon/daemon.go): the 409 that
// POST .../stop answers when the input box holds an unsent draft. Other 409s
// (for example "not running") must not offer Stop anyway.
export const DRAFT_ERROR = 'agent has an unsent draft; clear it or use force'
</script>

<script setup lang="ts">
// One terminal panel of mockup A: a two-row header (folder, status, waiting
// messages, Sessions / Stop / Maximize / Close; then account, default login
// warning, session) over an xterm.js terminal on /ws/term/{ws}/{name}.
//
// Socket protocol (internal/server/ws.go): the server sends the scrollback
// replay and then live output as binary frames; the panel sends text JSON
// {"type":"input","data":...} and {"type":"resize","cols":...,"rows":...}.
// The server clamps sizes (see MIN_COLS and MIN_ROWS), so the panel always
// sends the size it really fits.
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Terminal } from '@xterm/xterm'
import type { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, ApiError, openSocket } from '../api'
import { store } from '../stores/state'
import { panelStore } from '../stores/panels'
import { createMessages } from '../stores/messages'
import Icon from './Icon.vue'
import StatusBadge from './StatusBadge.vue'
import SessionPicker, { relTime } from './SessionPicker.vue'
import type { Session } from '../types'

const props = defineProps<{ ws: string; folder: string; focused: boolean; maximized: boolean; canMaximize: boolean }>()
const emit = defineEmits<{ maximize: []; restore: [] }>()

// Reconnect delays in ms, the same steps as the event socket; the last repeats.
const backoff = [500, 1000, 2000, 4000, 8000, 10000]
const fitDelay = 100
const OPEN = 1 // WebSocket.OPEN

const enc = encodeURIComponent
const base = computed(() => `/api/agents/${enc(props.ws)}/${enc(props.folder)}`)

const f = computed(
  () => store.state.data?.workspaces.find((w) => w.name === props.ws)?.folders.find((x) => x.name === props.folder) ?? null,
)
const status = computed(() => f.value?.status ?? 'stopped')
const running = computed(() => status.value !== 'stopped')
// The server never sends an account on windows; hide it there in any case.
const windows = computed(() => store.state.data?.platform === 'windows')

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

// ---- session title (the state only carries the id) ----
const session = ref<Session | null>(null)
const sessionId = computed(() => f.value?.session_id ?? '')
const sessionText = computed(() => session.value?.title || sessionId.value.slice(0, 8))

watch(
  // Read again when the agent stops, so "Last session" has a fresh time.
  () => `${sessionId.value}\n${running.value}`,
  async () => {
    const id = sessionId.value
    session.value = null
    if (!id) return
    try {
      const r = await api<{ sessions: Session[] }>(
        'GET',
        `/api/workspaces/${enc(props.ws)}/folders/${enc(props.folder)}/sessions`,
      )
      if (id === sessionId.value) session.value = r?.sessions?.find((s) => s.id === id) ?? null
    } catch {
      // The short id stands in for the title.
    }
  },
  { immediate: true },
)

const lastLine = computed(() => {
  const parts = [sessionId.value ? `Last session: ${sessionText.value}` : 'No session yet']
  if (session.value?.modified) parts.push(relTime(session.value.modified))
  const n = f.value?.pending ?? 0
  if (n > 0) parts.push(`${plural(n, 'message', 'messages')} waiting`)
  return parts.join(' · ')
})

// ---- messages held by a draft in this panel ----
// The folder state has no draft flag, so the panel follows its own
// workspace's messages (Task 18's store, scoped to this workspace) and
// counts the ones held by a draft for this folder.
const msgs = createMessages({
  api,
  store: { state: store.state, activeWorkspace: () => ({ name: props.ws, folders: [] }), onEvent: store.onEvent },
})
// The chip counts held_draft messages only. The sidebar's pending count also
// includes nudged messages, so the two numbers can differ.
const heldByDraft = computed(
  () => msgs.newestFirst.value.filter((m) => m.to === props.folder && m.state === 'held_draft').length,
)
const loadFailed = computed(() => msgs.error.value !== '')
const showWaiting = computed(() => status.value === 'idle' && (f.value?.pending ?? 0) > 0 && heldByDraft.value > 0)

// A narrow panel moves the waiting chip and Deliver now from the end of row 1
// to the end of row 2, so the header never gets a third row. 0 means "not
// laid out yet" and keeps the chip on row 1.
// Under TIGHT (a phone, or a 2x2 panel in a small window) the chip also
// drops its icons and the word "messages", so row 2 still fits.
const NARROW = 760
const TIGHT = 460
const narrow = ref(false)
const tight = ref(false)
function measure(): void {
  const w = pane.value?.getBoundingClientRect().width ?? 0
  narrow.value = w > 0 && w <= NARROW
  tight.value = w > 0 && w <= TIGHT
}

// ---- header actions ----
const alert = ref('')
const canForce = ref(false) // the last stop was refused because of a draft
const acting = ref(false)

async function act(fn: () => Promise<unknown>): Promise<ApiError | null> {
  alert.value = ''
  canForce.value = false
  acting.value = true
  try {
    await fn()
    return null
  } catch (e) {
    const err = e instanceof ApiError ? e : new ApiError(0, 'Something went wrong. Try again.')
    alert.value = err.message
    return err
  } finally {
    acting.value = false
  }
}

async function stop(force = false): Promise<void> {
  const err = await act(() => api('POST', `${base.value}/stop${force ? '?force=true' : ''}`))
  if (err?.status === 409 && err.message === DRAFT_ERROR && !force) {
    alert.value = 'This agent has an unsent draft. Stop anyway discards it and ends the current turn.'
    canForce.value = true
  }
}

function start(body: { session_id: string } | { new: true }): void {
  void act(() => api('POST', `${base.value}/start`, body))
}

function deliverNow(): void {
  void act(() => api('POST', `${base.value}/deliver-now`))
}

function close(): void {
  panelStore.closePanel(props.ws, props.folder)
}

function focusMe(): void {
  if (!props.focused) panelStore.focusPanel(props.ws, props.folder)
}

// ---- session picker popover ----
const pane = ref<HTMLElement | null>(null)
const header = ref<HTMLElement | null>(null)
const sessionsBtn = ref<HTMLButtonElement | null>(null)
const popWrap = ref<HTMLElement | null>(null)
const pickerOpen = ref(false)
const popPos = ref({ top: 38, caret: 120 })

function togglePicker(): void {
  pickerOpen.value = !pickerOpen.value
  if (!pickerOpen.value || !pane.value || !header.value || !sessionsBtn.value) return
  // Desktop: under the Sessions button, the caret pointing at its middle.
  // Phone: under the whole header, so the status line stays visible.
  const p = pane.value.getBoundingClientRect()
  const b = sessionsBtn.value.getBoundingClientRect()
  const phone = window.matchMedia?.('(max-width: 767px)').matches ?? false
  const below = phone ? header.value.getBoundingClientRect().bottom : b.bottom
  popPos.value = { top: Math.round(below - p.top + 6), caret: Math.round(p.right - (b.left + b.width / 2) - 11) }
}

function onOutside(ev: PointerEvent): void {
  const t = ev.target as Node | null
  if (!pickerOpen.value || !t) return
  if (popWrap.value?.contains(t) || sessionsBtn.value?.contains(t)) return
  pickerOpen.value = false
}

// ---- terminal ----
const host = ref<HTMLElement | null>(null)
let term: Terminal | null = null
let fit: FitAddon | null = null
let sock: WebSocket | null = null
let seq = 0 // bumped by every connect and disconnect; older attempts give up
let attempt = 0
let retryTimer: ReturnType<typeof setTimeout> | undefined
let fitTimer: ReturnType<typeof setTimeout> | undefined
let ro: ResizeObserver | null = null
let paneRo: ResizeObserver | null = null
let alive = true
let replaying = true // until xterm has written the first frame of this socket
let stableTimer: ReturnType<typeof setTimeout> | undefined
let gotReplay = false // the first frame of this socket has arrived

function token(name: string): string | undefined {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || undefined
}

// The font must be loaded before xterm measures its cell size.
async function loadFont(): Promise<void> {
  const font = token('--font')
  if (!font || !document.fonts) return
  try {
    await document.fonts.load(`${token('--fs-term') ?? '12px'} ${font}`)
  } catch {
    // xterm falls back to the next font in the list
  }
}

// xterm.js is loaded on first use: the app shell does not need it, and it
// touches canvas APIs as soon as its module runs.
async function createTerm(el: HTMLElement): Promise<void> {
  const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')])
  if (!alive || term) return
  term = new Terminal({
    fontFamily: token('--font'),
    fontSize: parseFloat(token('--fs-term') ?? '') || 12,
    lineHeight: 1, // more rows; the server needs at least MIN_ROWS
    cursorBlink: true,
    // OSC 8 hyperlinks from the agent: http and https only, never an opener.
    linkHandler: {
      activate(_ev: MouseEvent, text: string) {
        let u: URL
        try {
          u = new URL(text)
        } catch {
          return
        }
        if (u.protocol === 'http:' || u.protocol === 'https:') window.open(u.href, '_blank', 'noopener,noreferrer')
      },
    },
    scrollback: 5000,
    theme: {
      background: token('--term'),
      foreground: token('--text'),
      cursor: token('--text'),
      cursorAccent: token('--term'),
      selectionBackground: token('--muted-hover'),
    },
  })
  fit = new FitAddon()
  term.loadAddon(fit)
  term.open(el)
  term.onData((data) => {
    // While the replay is written, xterm answers the queries inside the old
    // output (DA, DSR, color reports). Those answers are not typing.
    if (replaying) return
    send({ type: 'input', data })
  })
}

function send(frame: object): void {
  if (sock && sock.readyState === OPEN) sock.send(JSON.stringify(frame))
}

function wanted(): boolean {
  return alive && running.value
}

async function connect(): Promise<void> {
  retryTimer = undefined
  if (!wanted()) return
  const mine = ++seq
  try {
    if (!term) {
      await loadFont()
      if (mine !== seq || !wanted() || !host.value) return
      await createTerm(host.value)
      if (mine !== seq || !wanted()) return
    }
    const s = await openSocket(`/ws/term/${enc(props.ws)}/${enc(props.folder)}`)
    if (mine !== seq || !wanted()) {
      s.close()
      return
    }
    attach(s)
  } catch {
    if (mine === seq) scheduleRetry()
  }
}

function scheduleRetry(): void {
  if (!wanted() || retryTimer !== undefined) return
  const delay = backoff[Math.min(attempt, backoff.length - 1)]
  attempt++
  retryTimer = setTimeout(() => void connect(), delay)
}

// The backoff goes back to its first step only when a connection proved
// stable: a frame after the replay, or the socket open for 3 s. The replay
// frame alone proves nothing, because a socket that closes at once still
// sends it.
const STABLE_MS = 3000

function attach(s: WebSocket): void {
  sock = s
  s.binaryType = 'arraybuffer'
  // Every connect starts with a full replay, so start from a clean screen.
  term?.reset()
  replaying = true
  gotReplay = false
  clearTimeout(stableTimer)
  stableTimer = setTimeout(() => {
    if (sock === s) attempt = 0
  }, STABLE_MS)
  s.onerror = null
  s.onmessage = (ev: MessageEvent) => {
    if (sock !== s || !term) return
    const data = typeof ev.data === 'string' ? ev.data : new Uint8Array(ev.data as ArrayBuffer)
    if (!gotReplay) {
      // The first frame is the replay. Input stays off until xterm has
      // parsed it, which is when its automatic replies are over.
      gotReplay = true
      if (data.length === 0) replaying = false
      else term.write(data, () => (replaying = false))
      return
    }
    attempt = 0
    term.write(data)
  }
  s.onclose = () => {
    if (sock !== s) return
    sock = null
    clearTimeout(stableTimer)
    scheduleRetry()
  }
  fitNow()
}

function disconnect(): void {
  seq++
  clearTimeout(retryTimer)
  clearTimeout(stableTimer)
  retryTimer = undefined
  attempt = 0
  const s = sock
  sock = null
  s?.close()
}

// fitNow sizes xterm to the panel and tells the server the new size.
function fitNow(): void {
  clearTimeout(fitTimer)
  fitTimer = undefined
  if (!term || !fit || !running.value) return
  const d = fit.proposeDimensions()
  if (!d || !(d.cols > 0) || !(d.rows > 0)) return // hidden or not laid out yet
  fit.fit()
  // The server clamps to at least MIN_COLS x MIN_ROWS, so xterm must draw
  // that grid or the TUI would be laid out for a size xterm does not show.
  // The frame still carries the real size; the extra scrolls inside .term.
  if (term.cols < MIN_COLS || term.rows < MIN_ROWS) {
    term.resize(Math.max(term.cols, MIN_COLS), Math.max(term.rows, MIN_ROWS))
  }
  send({ type: 'resize', cols: d.cols, rows: d.rows })
}

function fitSoon(): void {
  clearTimeout(fitTimer)
  fitTimer = setTimeout(fitNow, fitDelay)
}

// Runs after the DOM update, so the terminal box is visible when it connects.
watch(running, (r) => {
  if (r) void connect()
  else disconnect()
}, { flush: 'post' })

onMounted(() => {
  msgs.start()
  measure()
  if (typeof ResizeObserver !== 'undefined' && pane.value) {
    paneRo = new ResizeObserver(() => measure())
    paneRo.observe(pane.value)
  }
  document.addEventListener('pointerdown', onOutside)
  if (typeof ResizeObserver !== 'undefined' && host.value) {
    ro = new ResizeObserver(() => fitSoon())
    ro.observe(host.value)
  }
  void nextTick(() => {
    if (running.value) void connect()
  })
})

onBeforeUnmount(() => {
  alive = false
  msgs.stop()
  document.removeEventListener('pointerdown', onOutside)
  disconnect()
  clearTimeout(fitTimer)
  ro?.disconnect()
  paneRo?.disconnect()
  term?.dispose()
  term = null
  fit = null
})
</script>

<template>
  <div
    ref="pane"
    class="pane"
    :class="{ focus: focused, attn: status === 'attention', 'pane-stopped': !running, tight }"
    @pointerdown="focusMe"
    @focusin="focusMe"
  >
    <div ref="header" class="ph">
      <div class="ph-1">
        <span class="ph-name">{{ folder }}</span>
        <StatusBadge :status="status" />
        <span v-if="showWaiting && !narrow" class="waiting">
          <span class="chip" title="Held because you have an unsent draft in this panel">
            <Icon name="msg" />{{ heldByDraft }} <span class="opt">{{ heldByDraft === 1 ? 'message' : 'messages' }}</span> waiting
          </span>
          <button class="btn act" type="button" :disabled="acting" @click="deliverNow"><Icon name="right" />Deliver now</button>
        </span>
        <div class="ph-btns">
          <button
            ref="sessionsBtn"
            class="ib sessions-btn"
            type="button"
            aria-label="Sessions"
            title="Sessions"
            aria-haspopup="dialog"
            :aria-expanded="pickerOpen ? 'true' : 'false'"
            @click="togglePicker"
          >
            <Icon name="history" /><span class="lab">Sessions</span>
          </button>
          <button
            class="ib"
            type="button"
            aria-label="Stop agent"
            :title="running ? 'Stop agent' : 'Already stopped'"
            :disabled="!running || acting"
            @click="stop()"
          >
            <Icon name="square" />
          </button>
          <button
            v-if="maximized"
            class="ib"
            type="button"
            aria-label="Restore layout"
            title="Restore layout"
            @click="emit('restore')"
          >
            <Icon name="restore" />
          </button>
          <button
            v-else
            class="ib max-btn"
            type="button"
            aria-label="Maximize"
            :title="canMaximize ? 'Maximize' : 'Only one panel is open'"
            :disabled="!canMaximize"
            @click="emit('maximize')"
          >
            <Icon name="max" />
          </button>
          <button
            class="ib"
            type="button"
            aria-label="Close panel"
            title="Close panel. The agent keeps running."
            @click="close"
          >
            <Icon name="x" />
          </button>
        </div>
      </div>
      <div class="ph-2">
        <span v-if="!windows" class="acct"><Icon name="user" /><b v-if="f?.account">{{ f.account }}</b><template v-else>no account</template></span>
        <span v-if="!windows && f?.on_default_login" class="warn-inline" title="On default login: a cswap switch will move it">
          <Icon name="attn" />on default login
        </span>
        <span v-if="sessionText" class="sess-name"><Icon name="hash" /><span class="t">{{ sessionText }}</span></span>
        <span v-if="showWaiting && narrow" class="waiting">
          <span class="chip" title="Held because you have an unsent draft in this panel">
            <Icon name="msg" />{{ heldByDraft }} <span class="opt">{{ heldByDraft === 1 ? 'message' : 'messages' }}</span> waiting
          </span>
          <button class="btn act" type="button" :disabled="acting" @click="deliverNow"><Icon name="right" />Deliver now</button>
        </span>
      </div>
    </div>

    <div v-if="alert" class="p-alert" role="alert">
      <Icon name="attn" />
      <span class="msg">{{ alert }}</span>
      <button v-if="canForce" class="btn btn-danger force" type="button" :disabled="acting" @click="stop(true)">Stop anyway</button>
      <button class="ib" type="button" aria-label="Dismiss" title="Dismiss" @click="alert = ''; canForce = false">
        <Icon name="x" />
      </button>
    </div>

    <div v-if="loadFailed" class="p-note" role="status">
      <Icon name="attn" />Held messages could not be loaded, so the waiting count may be missing.
    </div>

    <div class="body">
      <div v-show="running" ref="host" class="term"></div>
      <div v-if="!running" class="stopped">
        <div class="big c-stop"><Icon name="stop" />Stopped</div>
        <div class="c-muted">{{ lastLine }}</div>
        <div class="acts">
          <button v-if="sessionId" class="btn btn-primary resume" type="button" :disabled="acting" @click="start({ session_id: sessionId })">
            <Icon name="play" />Resume session
          </button>
          <button class="btn new" :class="{ 'btn-primary': !sessionId }" type="button" :disabled="acting" @click="start({ new: true })">
            <Icon name="plus" />New session
          </button>
        </div>
      </div>
    </div>

    <div
      v-if="pickerOpen"
      ref="popWrap"
      class="pop-wrap"
      :style="{ top: `${popPos.top}px`, '--caret-right': `${popPos.caret}px` }"
    >
      <SessionPicker :ws="ws" :folder="folder" @close="pickerOpen = false" />
    </div>
  </div>
</template>

<style scoped>
.pane {
  position: relative; flex: 1;
  display: flex; flex-direction: column; min-height: 0; min-width: 0;
  border: 1px solid var(--border); border-radius: var(--r); background: var(--surface);
}
.pane.focus { border-color: var(--border-strong); }
.pane.attn { border-color: var(--amber-line); }

/* header */
.ph { border-bottom: 1px solid var(--border); padding: var(--s-1) var(--s-2) 3px var(--s-5); }
.ph-1 { display: flex; align-items: center; gap: var(--s-4); min-height: var(--row-h); flex-wrap: wrap; }
.ph-name { font-weight: 700; min-width: 0; overflow-wrap: anywhere; }
.waiting { display: inline-flex; align-items: center; gap: var(--s-3); flex: none; }
.ph-btns { margin-left: auto; display: flex; gap: var(--s-1); }
/* Row 2 never wraps: the header is exactly two rows. Long text is cut with
   an ellipsis (the session title first), and the waiting chip, which sits at
   the end of row 2 in a narrow panel, keeps its size. */
.ph-2 {
  display: flex; align-items: center; flex-wrap: nowrap; gap: 14px; color: var(--text-muted);
  padding-right: var(--s-3); min-width: 0;
}
.ph-2 > span:not(.waiting) {
  display: block; min-width: 0; flex: 0 1 auto; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.ph-2 > .sess-name:not(.waiting) { flex-shrink: 3; }
.ph-2 > .acct:not(.waiting), .ph-2 > .warn-inline:not(.waiting) { flex-shrink: 0; }
.ph-2 > span:not(.waiting) .i { vertical-align: -2px; margin-right: 5px; }
.ph-2 .waiting { margin-left: auto; }
.ph-2 b { color: var(--text); font-weight: 500; }
.warn-inline { color: var(--amber); }

/* Row 2 is tight in a very narrow panel: the chip says "3 waiting" (the word
   "messages" stays for screen readers) and the row-2 icons go. */
.tight .ph-2 { gap: var(--s-4); }
.tight .waiting .opt {
  position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap;
}
.tight .ph-2 .waiting .i { display: none; }

/* inline error under the header */
.p-alert {
  display: flex; align-items: center; gap: var(--s-3); flex-wrap: wrap;
  margin: var(--s-2) var(--s-3) 0; padding: var(--s-1) var(--s-1) var(--s-1) var(--s-4);
  border: 1px solid var(--red-line); background: var(--red-tint); color: var(--red); border-radius: var(--r-sm);
}
.p-alert .msg { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.p-alert .ib { color: var(--red); }
.p-note {
  display: flex; align-items: center; gap: var(--s-3);
  margin: var(--s-2) var(--s-3) 0; padding: var(--s-1) var(--s-4);
  border: 1px solid var(--amber-line); background: var(--amber-tint); color: var(--amber); border-radius: var(--r-sm);
}
.btn:disabled { opacity: .6; cursor: default; }

/* terminal body: padding sits here, never on the xterm host, so the fit
   addon measures the real box */
.body {
  flex: 1; min-height: 0; display: flex; flex-direction: column;
  background: var(--term); border-radius: 0 0 var(--r) var(--r);
  padding: var(--s-3) var(--s-2) var(--s-2) var(--s-5);
}
/* When the panel is smaller than the PTY minimum, xterm draws the minimum
   and the overflow scrolls here, never the page. */
.term { flex: 1; min-height: 0; min-width: 0; overflow: auto; }

.stopped {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: var(--s-3);
  text-align: center; padding: var(--s-5);
}
.stopped .big { display: flex; align-items: center; gap: var(--s-4); font-weight: 700; font-size: var(--fs-lg); }
.stopped .acts { display: flex; flex-wrap: wrap; gap: var(--s-4); justify-content: center; margin-top: var(--s-2); }

/* session picker popover, under the Sessions button */
.pop-wrap { position: absolute; z-index: 5; right: var(--s-3); max-width: calc(100% - 12px); }
.pop-wrap::before {
  content: ""; position: absolute; top: -6px; right: var(--caret-right, 120px); width: 10px; height: 10px;
  background: var(--muted); border-left: 1px solid var(--border-strong); border-top: 1px solid var(--border-strong);
  transform: rotate(45deg);
}

@media (max-width: 767px) {
  .pane { height: 360px; flex: 1 1 auto; }
  .pane.pane-stopped { height: auto; }
  .pane.focus { height: 400px; }
  .pane.focus.pane-stopped { height: auto; }
  .ph-1 { min-height: var(--touch-h); row-gap: var(--s-1); padding-bottom: var(--s-2); }
  .ph-1 .ph-name { order: 0; flex: 1 0 calc(100% - 190px); }
  .ph-1 .ph-btns { order: 1; }
  .ph-1 :deep(.badge) { order: 2; }
  .ib .lab { display: none; }
  /* panels are stacked and all shown, so there is nothing to maximize */
  .max-btn { display: none; }
  .stopped { padding: 20px var(--s-5); }
  .pop-wrap { left: var(--s-3); }
  .pop-wrap::before { display: none; }
}
</style>
