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
import { api, ApiError, IMAGE_TOO_LARGE, MAX_IMAGE_BYTES, openSocket, uploadAttachment } from '../api'
import { store } from '../stores/state'
import { panelStore } from '../stores/panels'
import { acquireMessages } from '../stores/messages'
import Icon from './Icon.vue'
import StatusBadge from './StatusBadge.vue'
import SessionPicker, { relTime } from './SessionPicker.vue'
import type { Session } from '../types'
import { detectOS, keyAction, type TermOS } from '../termkeys'
import { prefs } from '../stores/prefs'

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
// workspace's messages (the shared per-workspace store) and
// counts the ones held by a draft for this folder.
// One store per workspace is shared by every panel (see acquireMessages).
const held = acquireMessages(props.ws)
const msgs = held.messages
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
// More terminal to the right than the box shows (xterm is at least MIN_COLS
// wide): a fade on the right edge says it scrolls. Overlay scrollbars hide
// themselves, so the scrollbar alone is not a cue.
const moreRight = ref(false)
function measureScroll(): void {
  const el = host.value
  moreRight.value = !!el && el.scrollWidth - el.clientWidth - el.scrollLeft > 1
}
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

// A stop answer belongs to the agent it was sent for. When the status or the
// session changes, the old alert and its "Stop anyway" are dropped, so a stale
// button can never force-kill a newer agent.
watch(
  () => `${status.value}\n${sessionId.value}\n${running.value}`,
  () => {
    alert.value = ''
    canForce.value = false
    resetConfirm()
  },
)

// A busy agent is mid-turn: the first click on Stop only asks, and the ask
// goes away by itself after CONFIRM_MS.
const CONFIRM_MS = 4000
const confirming = ref(false)
let confirmTimer: ReturnType<typeof setTimeout> | undefined
function resetConfirm(): void {
  clearTimeout(confirmTimer)
  confirmTimer = undefined
  confirming.value = false
}
function onStop(): void {
  if (status.value === 'busy' && !confirming.value) {
    confirming.value = true
    confirmTimer = setTimeout(resetConfirm, CONFIRM_MS)
    return
  }
  resetConfirm()
  void stop()
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

// The panel only offers Deliver now while a draft holds messages, and the
// daemon submits that draft together with the nudge. So the first click asks
// and the second one delivers; the ask goes away by itself.
const deliverTitle = 'Deliver now. The draft in the input box is submitted together with the nudge.'
const deliverAsk = ref(false)
let deliverTimer: ReturnType<typeof setTimeout> | undefined
function resetDeliverAsk(): void {
  clearTimeout(deliverTimer)
  deliverTimer = undefined
  deliverAsk.value = false
}
function deliverNow(): void {
  if (!deliverAsk.value) {
    deliverAsk.value = true
    deliverTimer = setTimeout(resetDeliverAsk, CONFIRM_MS)
    return
  }
  resetDeliverAsk()
  void act(() => api('POST', `${base.value}/deliver-now`))
}
watch(showWaiting, (v) => {
  if (!v) resetDeliverAsk()
})

function close(): void {
  panelStore.closePanel(props.ws, props.folder)
}

// Gives xterm the keyboard when this panel is the focused one, for example
// after a click in the sidebar. It does not take focus away from a control of
// this panel that the user is using (a header button reached with Tab).
function focusTerm(): void {
  if (!term || !props.focused) return
  const a = document.activeElement
  if (a && pane.value?.contains(a) && !host.value?.contains(a)) return
  term.focus()
}

watch(() => props.focused, () => focusTerm(), { flush: 'post' })

function focusMe(): void {
  if (!props.focused) panelStore.focusPanel(props.ws, props.folder)
}

// ---- image drop and paste ----
// A browser gives the page the bytes of a dropped or pasted image, never a
// path. The daemon saves the image for this agent and answers its path; the
// panel pastes that path, and Claude Code turns it into [Image #N].
const NEED_RUNNING = 'Start the agent to attach images.'
const IMAGES_ONLY = 'Only images can be attached (PNG, JPEG, GIF, WebP).'
const TOO_LARGE = IMAGE_TOO_LARGE
const RECONNECTING = 'The terminal is reconnecting, so the image was not attached. Drop it again.'
const dropping = ref(false)
let dragDepth = 0 // dragenter/dragleave also fire for every child element

function hasFiles(ev: DragEvent): boolean {
  return !!ev.dataTransfer?.types?.includes('Files')
}
function onDragEnter(ev: DragEvent): void {
  if (!hasFiles(ev)) return
  ev.preventDefault()
  dragDepth++
  dropping.value = true
}
function onDragOver(ev: DragEvent): void {
  if (!hasFiles(ev)) return
  ev.preventDefault()
  if (ev.dataTransfer) ev.dataTransfer.dropEffect = 'copy'
}
function onDragLeave(ev: DragEvent): void {
  if (!hasFiles(ev)) return
  dragDepth = Math.max(0, dragDepth - 1)
  if (dragDepth === 0) dropping.value = false
}
function onDrop(ev: DragEvent): void {
  if (!hasFiles(ev)) return
  ev.preventDefault()
  dragDepth = 0
  dropping.value = false
  void attachFiles(Array.from(ev.dataTransfer?.files ?? []))
}

// Runs before xterm's own paste handler (capture phase). It takes the paste
// only when the clipboard holds image files and no text: a copy from Office
// or a web page carries both, and then the text is what the user wants.
function onPaste(ev: ClipboardEvent): void {
  const cd = ev.clipboardData
  if (!cd) return
  const images = Array.from(cd.files ?? []).filter((f) => f.type.startsWith('image/'))
  if (images.length === 0 || cd.getData('text/plain').trim() !== '') return
  ev.preventDefault()
  ev.stopPropagation()
  void attachFiles(images)
}

// The panel alert is shared with Stop. An attach message only replaces an
// earlier attach message, so a pending "Stop anyway" is never hidden (#73).
let attachAlert = false
function showAttach(msg: string): void {
  if (alert.value !== '' && !attachAlert) return
  alert.value = msg
  attachAlert = true
}
watch(alert, (v) => {
  if (v === '') attachAlert = false
})

// Uploads the images one by one, in order, and pastes each path. The first
// error stops the rest and shows in the panel alert. The size limit is
// checked here too, so a large file never starts an upload that the daemon
// would cut off mid-body (#71).
async function attachFiles(files: File[]): Promise<void> {
  if (files.length === 0) return
  if (attachAlert) alert.value = ''
  if (!running.value) {
    showAttach(NEED_RUNNING)
    return
  }
  const images = files.filter((f) => f.type.startsWith('image/'))
  if (images.length < files.length) showAttach(IMAGES_ONLY)
  const fit = images.filter((f) => f.size <= MAX_IMAGE_BYTES)
  if (fit.length < images.length) showAttach(TOO_LARGE)
  for (const f of fit) {
    try {
      const p = await uploadAttachment(props.ws, props.folder, f)
      // A reconnecting socket would drop the paste without a word.
      if (!term || !sock || sock.readyState !== OPEN || replaying) {
        showAttach(RECONNECTING)
        return
      }
      term.paste(p)
    } catch (e) {
      showAttach(e instanceof ApiError ? e.message : 'Something went wrong. Try again.')
      return
    }
  }
  focusTerm()
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

// ---- OSC 8 link confirm ----
const linkUrl = ref('')
const linkCancel = ref<HTMLButtonElement | null>(null)

function askLink(href: string): void {
  linkUrl.value = href
  void nextTick(() => linkCancel.value?.focus())
}
function closeLink(): void {
  linkUrl.value = ''
  focusTerm()
}
function openLink(): void {
  const href = linkUrl.value
  linkUrl.value = ''
  window.open(href, '_blank', 'noopener,noreferrer')
  focusTerm()
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
    await document.fonts.load(`${prefs.termFont}px ${font}`)
  } catch {
    // xterm falls back to the next font in the list
  }
}

// Writes the selection to the clipboard. On Windows the selection is cleared
// after a copy, like Windows Terminal; Linux terminals keep it. If the write
// is refused, the selection stays so the user can try again.
function copySelection(t: Terminal, os: TermOS): void {
  const sel = t.getSelection()
  if (!sel) return
  navigator.clipboard.writeText(sel).then(
    () => {
      if (os === 'windows') t.clearSelection()
    },
    () => {},
  )
}

// xterm.js is loaded on first use: the app shell does not need it, and it
// touches canvas APIs as soon as its module runs.
async function createTerm(el: HTMLElement): Promise<void> {
  const [{ Terminal }, { FitAddon }] = await Promise.all([import('@xterm/xterm'), import('@xterm/addon-fit')])
  if (!alive || term) return
  term = new Terminal({
    fontFamily: token('--font'),
    fontSize: prefs.termFont, // the top bar size control (stores/prefs.ts)
    lineHeight: 1, // more rows; the server needs at least MIN_ROWS
    cursorBlink: true,
    // OSC 8 hyperlinks from the agent: http and https only. The agent chose
    // the link, so the user sees the real URL and confirms before it opens.
    linkHandler: {
      activate(_ev: MouseEvent, text: string) {
        let u: URL
        try {
          u = new URL(text)
        } catch {
          return
        }
        if (u.protocol === 'http:' || u.protocol === 'https:') askLink(u.href)
      },
    },
    scrollback: 5000,
    // Claude Code turns on mouse tracking, so a plain drag goes to it. Shift
    // (Windows, Linux) or Option (macOS) + drag selects in the terminal (#68).
    macOptionClickForcesSelection: true,
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
  // Copy and paste follow the browser OS (see termkeys.ts). A paste is not
  // prevented: the browser fires 'paste' on xterm's textarea and xterm sends
  // it as a bracketed paste, so no clipboard permission prompt appears.
  const os = detectOS(navigator as Navigator & { userAgentData?: { platform?: string } })
  // The key a fresh keydown took, kept until its keyup. Repeats, keypress
  // and keyup of that key follow the first decision: after a copy on Windows
  // the selection is gone, and a repeat that decided again would send \x03.
  let held: { code: string; action: 'copy' | 'paste' } | null = null
  term.attachCustomKeyEventHandler((ev) => {
    const t = term
    if (!t) return true
    if (held && ev.code === held.code && (ev.type !== 'keydown' || ev.repeat)) {
      if (held.action === 'copy') ev.preventDefault()
      if (ev.type === 'keyup') held = null
      return false
    }
    const a = keyAction(os, ev, t.hasSelection())
    if (ev.type === 'keydown') held = a === 'pass' ? null : { code: ev.code, action: a }
    if (a === 'pass') return true
    if (a === 'copy') {
      ev.preventDefault() // also stops Chrome's Ctrl+Shift+C inspect
      if (ev.type === 'keydown') copySelection(t, os)
    }
    return false
  })
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
      else term.write(data, () => { if (sock === s) replaying = false })
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
  focusTerm()
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
  measureScroll()
}

function fitSoon(): void {
  clearTimeout(fitTimer)
  fitTimer = setTimeout(fitNow, fitDelay)
}

// A new text size from the top bar: xterm measures the new cell size once
// the font is loaded at that size, then the fit tells the server the grid.
watch(
  () => prefs.termFont,
  async (n) => {
    if (!term) return
    await loadFont()
    // Two quick changes can finish their font loads out of order: only the
    // newest size is applied (#81).
    if (!term || n !== prefs.termFont) return
    term.options.fontSize = n
    fitNow()
  },
)

// Runs after the DOM update, so the terminal box is visible when it connects.
watch(running, (r) => {
  if (r) void connect()
  else disconnect()
}, { flush: 'post' })

onMounted(() => {
  measure()
  host.value?.addEventListener('scroll', measureScroll, { passive: true })
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
  held.release()
  document.removeEventListener('pointerdown', onOutside)
  disconnect()
  clearTimeout(fitTimer)
  clearTimeout(confirmTimer)
  clearTimeout(deliverTimer)
  ro?.disconnect()
  paneRo?.disconnect()
  host.value?.removeEventListener('scroll', measureScroll)
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
    @dragenter="onDragEnter"
    @dragover="onDragOver"
    @dragleave="onDragLeave"
    @drop="onDrop"
    @paste.capture="onPaste"
  >
    <div ref="header" class="ph">
      <div class="ph-1">
        <span class="ph-name" :title="sessionText ? `${folder} · ${sessionText}` : folder">{{ folder }}</span>
        <StatusBadge :status="status" />
        <!-- One header row: these give way first, so the name, the status and
             the buttons always show. A narrow panel drops the session (it is
             in the name's tooltip), a very narrow one the account too: a
             stub like "# 5..." only makes the user guess. -->
        <span class="ph-meta">
          <span v-if="!windows && !tight" class="acct"><Icon name="user" /><b v-if="f?.account">{{ f.account }}</b><template v-else>no account</template></span>
          <span v-if="!windows && f?.on_default_login" class="warn-inline" title="On default login: a cswap switch will move it">
            <Icon name="attn" />on default login
          </span>
          <span v-if="sessionText && !narrow && !showWaiting" class="sess-name" :class="{ 'is-id': !session?.title }"><Icon name="hash" /><span class="t">{{ sessionText }}</span></span>
        </span>
        <span v-if="showWaiting && !narrow" class="waiting">
          <span class="chip" title="Held because you have an unsent draft in this panel">
            <Icon name="msg" />{{ heldByDraft }} <span class="opt">{{ heldByDraft === 1 ? 'message' : 'messages' }}</span> waiting
          </span>
          <button
            class="btn act"
            :class="{ ask: deliverAsk }"
            type="button"
            :aria-label="deliverAsk ? 'Submit the draft too? The draft in the input box is submitted together with the nudge.' : undefined"
            :title="deliverTitle"
            :disabled="acting"
            @click="deliverNow"
          ><Icon name="right" />{{ deliverAsk ? 'Submit draft too?' : 'Deliver now' }}</button>
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
            class="ib stop-btn"
            :class="{ confirm: confirming }"
            type="button"
            :aria-label="confirming ? 'Stop? It is working' : 'Stop agent'"
            :title="confirming ? 'Click again to stop the agent. It is working.' : running ? 'Stop agent' : 'Already stopped'"
            :disabled="!running || acting"
            @click="onStop"
          >
            <Icon name="square" /><span v-if="confirming" class="confirm-lab">Stop? It is working</span>
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
      <div v-if="showWaiting && narrow" class="ph-2 wrap">
        <span v-if="showWaiting && narrow" class="waiting">
          <span class="chip" title="Held because you have an unsent draft in this panel">
            <Icon name="msg" />{{ heldByDraft }} <span class="opt">{{ heldByDraft === 1 ? 'message' : 'messages' }}</span> waiting
          </span>
          <button
            class="btn act"
            :class="{ ask: deliverAsk }"
            type="button"
            :aria-label="deliverAsk ? 'Submit the draft too? The draft in the input box is submitted together with the nudge.' : undefined"
            :title="deliverTitle"
            :disabled="acting"
            @click="deliverNow"
          ><Icon name="right" />{{ deliverAsk ? 'Submit draft too?' : 'Deliver now' }}</button>
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
      <div v-if="running && moreRight" class="term-fade" aria-hidden="true"></div>
      <div v-if="dropping" class="drop-hint" role="status">
        <Icon name="image" />{{ running ? 'Drop image to attach' : NEED_RUNNING }}
      </div>
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
      v-if="linkUrl"
      class="link-ask"
      role="dialog"
      aria-label="Open link"
      @keydown.esc.stop="closeLink"
    >
      <p class="link-url">Open <b>{{ linkUrl }}</b>?</p>
      <div class="link-acts">
        <button class="btn btn-primary" type="button" @click="openLink"><Icon name="right" />Open</button>
        <button ref="linkCancel" class="btn" type="button" @click="closeLink">Cancel</button>
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
/* account, login warning and session title: they take the free space of row
   1 and are cut with an ellipsis first (the session title before the
   account), so the header stays one row. */
.ph-meta {
  flex: 1 1 0; min-width: 0; display: flex; align-items: center; gap: 14px;
  color: var(--text-muted); overflow: hidden;
}
.ph-meta > span { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 0 1 auto; }
.ph-meta > .sess-name { flex-shrink: 3; }
/* the session gives way before the account; an id is never cut to a stub
   (a title may lose its end), and the login warning, a safety cue, never
   shrinks (#80) */
.ph-meta > .sess-name.is-id { flex-shrink: 0; }
.ph-meta > .acct { flex-shrink: 1; }
.ph-meta > .warn-inline { flex-shrink: 0; }
.ph-meta > span .i { vertical-align: -2px; margin-right: 5px; }
.ph-meta b { color: var(--text); font-weight: 500; }
/* Row 2 exists only in a narrow panel with the waiting chip: the chip and
   its button take their own line at the right instead of being pushed out. */
.ph-2 {
  display: flex; align-items: center; justify-content: flex-end; flex-wrap: wrap;
  row-gap: var(--s-2); padding: 0 var(--s-3) var(--s-3) 0;
}
.ph-2 .waiting { margin-left: auto; }
.warn-inline { color: var(--amber); }

/* Deliver now is the drawer's button (same text color), trimmed to the chip's
   height so the two line up. Row 2 text is muted, so the color is set here. */
.waiting .chip { height: 24px; }
.waiting .btn.act { height: 24px; padding: 0 var(--s-4); color: var(--text); position: relative; }
/* The tap area stays 44px high on a phone without a taller box. */
@media (max-width: 767px) {
  .waiting .btn.act::after { content: ""; position: absolute; inset: -10px -4px; }
}

/* A very narrow panel: the chip says "3 waiting" (the word "messages" stays
   for screen readers) and drops its icon. If chip and button still do not
   fit next to the account, they take their own line at the right. */
.tight .ph { padding-bottom: var(--s-4); }
.tight .ph-2 { gap: var(--s-2) var(--s-4); padding-right: var(--s-8); }
.tight .waiting { gap: var(--s-4); }
.tight .waiting .opt {
  position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap;
}
.tight .waiting .chip .i { display: none; }

/* inline error under the header */
.p-alert {
  display: flex; align-items: center; gap: var(--s-3); flex-wrap: wrap;
  margin: var(--s-2) var(--s-3) 0; padding: var(--s-1) var(--s-1) var(--s-1) var(--s-4);
  border: 1px solid var(--red-line); background: var(--red-tint); color: var(--red); border-radius: var(--r-sm);
}
.p-alert .msg { flex: 1; min-width: 0; overflow-wrap: anywhere; }
.p-alert .ib { color: var(--red); }

/* Stop on a busy agent turns into a one-line confirm: red text and frame,
   with words, so it is not a color-only cue. */
.ib.confirm {
  gap: var(--s-2); padding: 0 var(--s-4); color: var(--red);
  border-color: var(--red-line); background: var(--red-tint);
}
.ib.confirm:hover:not(:disabled) { background: var(--red-tint); color: var(--red); border-color: var(--red); }
.confirm-lab { white-space: nowrap; font-weight: 600; }
.p-note {
  display: flex; align-items: center; gap: var(--s-3);
  margin: var(--s-2) var(--s-3) 0; padding: var(--s-1) var(--s-4);
  border: 1px solid var(--amber-line); background: var(--amber-tint); color: var(--amber); border-radius: var(--r-sm);
}
.btn:disabled { opacity: .6; cursor: default; }
/* the Deliver now ask: amber frame and words, like the Stop ask is red */
.waiting .btn.act.ask { color: var(--amber); border-color: var(--amber-line); background: var(--amber-tint); white-space: nowrap; }

/* terminal body: padding sits here, never on the xterm host, so the fit
   addon measures the real box */
.body {
  position: relative;
  flex: 1; min-height: 0; display: flex; flex-direction: column;
  background: var(--term); border-radius: 0 0 var(--r) var(--r);
  padding: var(--s-3) var(--s-2) var(--s-2) var(--s-5);
}
/* When the panel is smaller than the PTY minimum, xterm draws the minimum
   and the overflow scrolls here, never the page. */
.term {
  flex: 1; min-height: 0; min-width: 0; overflow: auto;
  /* a thin visible scrollbar, so a terminal wider than its box shows it can scroll */
  scrollbar-width: thin; scrollbar-color: var(--border-strong) transparent;
}

.term-fade {
  position: absolute; top: 0; right: 0; bottom: 0; width: 20px; pointer-events: none;
  border-radius: 0 0 var(--r) 0;
  /* a soft shade of the terminal's own color, not a lighter band */
  background: linear-gradient(to right, transparent, color-mix(in srgb, #000 55%, var(--term)));
}

/* image drop target over the terminal; it never takes pointer events, so the
   drag enter and leave counting stays on the panel */
.drop-hint {
  position: absolute; inset: var(--s-2); z-index: 4; pointer-events: none;
  display: flex; align-items: center; justify-content: center; gap: var(--s-4);
  border: 2px dashed var(--border-strong); border-radius: var(--r);
  background: color-mix(in srgb, var(--surface) 88%, transparent);
  color: var(--text); font-weight: 600; text-align: center; padding: var(--s-5);
}
.drop-hint .i { width: 20px; height: 20px; flex: none; }

.stopped {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: var(--s-3);
  text-align: center; padding: var(--s-5);
}
.stopped .big { display: flex; align-items: center; gap: var(--s-4); font-weight: 700; font-size: var(--fs-lg); }
.stopped .acts { display: flex; flex-wrap: wrap; gap: var(--s-4); justify-content: center; margin-top: var(--s-2); }

/* OSC 8 link confirm: over the bottom of the terminal, the whole URL wraps */
.link-ask {
  position: absolute; z-index: 6; left: var(--s-3); right: var(--s-3); bottom: var(--s-3);
  display: grid; gap: var(--s-3); padding: var(--s-4) var(--s-5);
  background: var(--muted); border: 1px solid var(--border-strong); border-radius: var(--r);
}
.link-url { margin: 0; overflow-wrap: anywhere; max-height: 6.5em; overflow-y: auto; }
.link-url b { font-weight: 600; }
.link-acts { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--s-4); }

/* session picker popover, under the Sessions button */
.pop-wrap { position: absolute; z-index: 5; right: var(--s-3); max-width: calc(100% - 12px); }
.pop-wrap::before {
  content: ""; position: absolute; top: -6px; right: var(--caret-right, 120px); width: 10px; height: 10px;
  background: var(--muted); border-left: 1px solid var(--border-strong); border-top: 1px solid var(--border-strong);
  transform: rotate(45deg);
}

@media (max-width: 767px) {
  .ph { padding-bottom: var(--s-4); }
  .pane { height: 360px; flex: 1 1 auto; }
  .pane.pane-stopped { height: auto; }
  .pane.focus { height: 400px; }
  .pane.focus.pane-stopped { height: auto; }
  .ph-1 { min-height: var(--touch-h); row-gap: var(--s-1); padding-bottom: var(--s-2); }
  .ph-1 .ph-name { order: 0; flex: 1 0 calc(100% - 190px); }
  .ph-1 .ph-btns { order: 1; }
  /* a phone has room below the name row, not beside it */
  .ph-1 .ph-meta { order: 3; flex-basis: 100%; }
  .ph-1 :deep(.badge) { order: 2; }
  .ib .lab { display: none; }
  /* panels are stacked and all shown, so there is nothing to maximize */
  .max-btn { display: none; }
  .stopped { padding: 20px var(--s-5); }
  .pop-wrap { left: var(--s-3); }
  .pop-wrap::before { display: none; }
  .link-acts .btn { flex: 1; justify-content: center; min-height: var(--touch-h); }
}
</style>
