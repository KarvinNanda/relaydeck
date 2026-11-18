<script setup lang="ts">
// The Messages timeline, newest first: time, from -> to, message, state.
// Rows that need the user carry their actions. Message text is written by
// agents, so it is rendered only through text interpolation.
import { reactive, watch } from 'vue'
import { ApiError } from '../api'
import { clock, type Messages } from '../stores/messages'
import type { Message, MessageState } from '../types'
import Icon, { type IconName } from './Icon.vue'

const props = defineProps<{ messages: Messages }>()

// Every state of internal/notify/router.go. The word carries the meaning;
// the icon and the color repeat it.
const states: Record<MessageState, { icon: IconName; cls: string; label: (m: Message) => string }> = {
  queued: { icon: 'clock', cls: 'c-muted', label: () => 'queued' },
  held_draft: { icon: 'clock', cls: 'c-idle', label: () => 'waiting (draft)' },
  held_external: { icon: 'pause', cls: 'c-attn', label: () => 'held: session open in terminal' },
  nudged: { icon: 'msg', cls: 'c-idle', label: () => 'nudged' },
  delivered: { icon: 'checks', cls: 'c-busy', label: () => 'delivered (read)' },
  awaiting_approval: { icon: 'shield', cls: 'c-attn', label: (m) => `awaiting approval (hop ${m.hop})` },
  rejected: { icon: 'x', cls: 'c-stop', label: () => 'rejected' },
}
const unknown = { icon: 'msg' as IconName, cls: 'c-muted', label: (m: Message) => String(m.state) }
const stateOf = (m: Message) => states[m.state] ?? unknown

const previewChars = 120

// preview is the first 120 characters on one line. Array.from counts code
// points, so an emoji or a CJK letter is never cut in half.
function preview(body: string): string {
  const chars = Array.from(body.replace(/\s+/g, ' ').trim())
  return chars.length > previewChars ? chars.slice(0, previewChars).join('') + '…' : chars.join('')
}

const open = reactive(new Set<string>())
const busy = reactive(new Set<string>())
const waited = reactive(new Set<string>())
// An error belongs to the state the row had when the action failed.
const errors = reactive(new Map<string, { text: string; state: MessageState }>())

function toggle(id: string): void {
  if (open.has(id)) open.delete(id)
  else open.add(id)
}

// A click on the text also toggles, unless the user just selected text.
function clickText(id: string): void {
  if (window.getSelection?.()?.toString()) return
  toggle(id)
}

const hasActions = (m: Message) =>
  m.state === 'awaiting_approval' || m.state === 'held_draft' || (m.state === 'held_external' && !waited.has(m.id))

function errorOf(m: Message): string | null {
  const e = errors.get(m.id)
  return e && e.state === m.state && hasActions(m) ? e.text : null
}

const errId = (m: Message) => `rd-msg-err-${encodeURIComponent(m.id)}`

// A state change makes an old error wrong, even if the row comes back to
// the same state later.
watch(
  () => props.messages.newestFirst.value,
  (list) => {
    for (const [id, e] of errors) if (list.find((m) => m.id === id)?.state !== e.state) errors.delete(id)
  },
)

type Action = 'approve' | 'reject' | 'start' | 'deliver'

// friendly turns a failed action into plain words. The server's own text
// goes to the browser console only.
function friendly(a: Action, e: unknown): string {
  const status = e instanceof ApiError ? e.status : -1
  if (status === 0) return (e as ApiError).message // written by api.ts, not by the server
  const gone = 'This folder is no longer in the workspace.'
  switch (a) {
    case 'approve':
    case 'reject':
      if (status === 409) return `Already handled, nothing to ${a}.`
      if (status === 404) return 'This message no longer exists.'
      return `Could not ${a}. See the relaydeck log.`
    case 'start':
      if (status === 409) return 'This folder already has a session running. Stop it first, then try again.'
      if (status === 404) return gone
      return 'Could not start a new session. See the relaydeck log.'
    case 'deliver':
      if (status === 404) return gone
      return 'Could not deliver now. See the relaydeck log.'
  }
}

async function act(m: Message, a: Action, run: () => Promise<unknown>): Promise<void> {
  busy.add(m.id)
  errors.delete(m.id)
  try {
    await run()
  } catch (e) {
    console.warn(`relaydeck: ${a} failed`, e instanceof ApiError ? e.status : -1, e instanceof Error ? e.message : String(e))
    errors.set(m.id, { text: friendly(a, e), state: m.state })
  } finally {
    busy.delete(m.id)
  }
}

const approve = (m: Message) => act(m, 'approve', () => props.messages.approve(m.id))
const reject = (m: Message) => act(m, 'reject', () => props.messages.reject(m.id))
const startNew = (m: Message) => act(m, 'start', () => props.messages.startNew(m))
const deliverNow = (m: Message) => act(m, 'deliver', () => props.messages.deliverNow(m))
</script>

<template>
  <div class="tl">
    <div class="tbl" role="table" aria-label="Timeline, newest first">
      <!-- Column widths come from the rows, so no header without rows. -->
      <div v-if="messages.newestFirst.value.length > 0" class="tr th" role="row">
        <span role="columnheader">time</span>
        <span role="columnheader">from -&gt; to</span>
        <span role="columnheader" class="h-msg">message</span>
        <span role="columnheader">state</span>
      </div>
      <div
        v-for="m in messages.newestFirst.value"
        :key="m.id"
        class="tr"
        :class="{ hot: messages.lit.has(m.id), open: open.has(m.id) }"
        role="row"
      >
        <span class="time" role="cell">{{ clock(m.created) }}</span>
        <span class="route" role="cell" :title="`${m.from} -> ${m.to}`">{{ m.from }} -&gt; {{ m.to }}</span>
        <span class="cell-msg" role="cell">
          <button class="exp" type="button" :aria-label="`Full message from ${m.from} to ${m.to}`" :aria-expanded="open.has(m.id)" @click="toggle(m.id)">
            <Icon :name="open.has(m.id) ? 'down' : 'right'" />
          </button>
          <span class="lbl">message: </span>
          <span class="body" @click="clickText(m.id)">{{ open.has(m.id) ? m.body : preview(m.body) }}</span>
        </span>
        <span class="state" role="cell">
          <span class="st" :class="stateOf(m).cls"><Icon :name="stateOf(m).icon" />{{ stateOf(m).label(m) }}</span>
          <span v-if="m.state === 'awaiting_approval'" class="acts">
            <button class="btn btn-primary act" type="button" :disabled="busy.has(m.id)" :aria-describedby="errorOf(m) ? errId(m) : undefined" @click="approve(m)">
              <Icon name="check" />Approve
            </button>
            <button class="btn btn-danger act" type="button" :disabled="busy.has(m.id)" :aria-describedby="errorOf(m) ? errId(m) : undefined" @click="reject(m)">
              <Icon name="x" />Reject
            </button>
          </span>
          <span v-else-if="m.state === 'held_external' && !waited.has(m.id)" class="acts">
            <button class="btn act" type="button" :disabled="busy.has(m.id)" :aria-describedby="errorOf(m) ? errId(m) : undefined" @click="startNew(m)">
              <Icon name="play" />Start new session
            </button>
            <button class="btn act" type="button" :disabled="busy.has(m.id)" @click="waited.add(m.id)">Wait</button>
          </span>
          <span v-else-if="m.state === 'held_draft'" class="acts">
            <button class="btn act" type="button" :disabled="busy.has(m.id)" :aria-describedby="errorOf(m) ? errId(m) : undefined" @click="deliverNow(m)">
              <Icon name="right" />Deliver now
            </button>
          </span>
          <span v-if="errorOf(m)" :id="errId(m)" class="err c-stop" role="alert"><Icon name="attn" />{{ errorOf(m) }}</span>
        </span>
      </div>
    </div>
    <p v-if="messages.error.value" class="empty err-line c-stop" role="alert">
      <Icon name="attn" />Could not load the messages.
      <button class="btn" type="button" @click="messages.reload()">Retry</button>
    </p>
    <p v-else-if="messages.newestFirst.value.length === 0" class="empty">
      {{ messages.loading.value ? 'Loading messages...' : 'No messages yet.' }}
    </p>
  </div>
</template>

<style scoped>
.tl { --inset: var(--s-6); overflow: auto; padding: var(--s-1) 0 var(--s-3); min-width: 0; }

/* Desktop: one grid for all rows (subgrid), so time, route and state take
   only the width their content needs and the message column gets the rest.
   It never drops under 20 characters plus the chevron. */
.tbl {
  display: grid; column-gap: var(--s-4);
  grid-template-columns: max-content minmax(0, max-content) minmax(calc(20ch + 18px), 1fr) minmax(0, max-content);
}
.tr {
  grid-column: 1 / -1; display: grid; grid-template-columns: subgrid;
  align-items: start; padding: var(--s-1) var(--inset); border-bottom: 1px solid var(--border);
}
.tr:last-child { border-bottom: 0; }
.tr.th { color: var(--text-muted); }
.tr.hot { background: var(--green-tint); }
.h-msg { padding-left: 18px; }
.time { color: var(--text-muted); }
.route { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

.cell-msg { display: flex; align-items: flex-start; gap: var(--s-2); min-width: 0; }
.exp {
  /* 24x24 hit area; the negative margins keep the 14x18 footprint of the row */
  flex: none; width: 24px; height: 24px; padding: 0; margin: -3px -5px -3px -5px;
  display: inline-grid; place-items: center;
  border: 0; border-radius: var(--r-sm); background: none; color: var(--text-muted); cursor: pointer;
}
.exp:hover { color: var(--text); }
.body { min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; cursor: pointer; }
.tr.open .body { white-space: pre-wrap; overflow-wrap: anywhere; cursor: text; }
.lbl { display: none; color: var(--text-muted); }
/* An open message gets its own line under the row, across route, message
   and state, so long text is not squeezed into the message column. */
.state { grid-column: 4; grid-row: 1; }
.tr.open .cell-msg { grid-column: 2 / -1; grid-row: 2; padding: var(--s-1) 0 var(--s-2); }

.state { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s-2) var(--s-3); min-width: 0; }
.st, .err { display: inline-flex; align-items: flex-start; gap: 5px; }
.st .i, .err .i { margin-top: 2px; }
.err { overflow-wrap: anywhere; }
.acts { display: flex; flex-wrap: wrap; gap: var(--s-3); width: 100%; }
.act:disabled { opacity: .55; cursor: progress; }

.empty { margin: 0; padding: var(--s-4) var(--inset); }
.err-line { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s-3); }

/* Phone: each message is a compact card, about 23px per line. The chevron
   is small, but its tap area covers the whole message line (44px). */
@media (max-width: 767px) {
  .tl { --inset: var(--gutter-mobile); overflow: visible; }
  .tbl { display: block; }
  .tr { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: var(--s-2) var(--s-5); padding: var(--s-2) var(--inset); }
  .tr.th { display: none; }
  .cell-msg, .state, .tr.open .cell-msg { grid-column: 1 / -1; grid-row: auto; padding: 0; }
  .cell-msg { display: block; position: relative; z-index: 0; }
  /* 24px box, but its margins keep the line at its normal height */
  .exp { width: 24px; height: 24px; margin: -2.5px var(--s-1) -2.5px -5px; vertical-align: top; position: static; }
  .exp::after { content: ''; position: absolute; left: 0; right: 0; top: -12px; bottom: -12px; z-index: -1; }
  .lbl { display: inline; }
  .body { white-space: normal; overflow-wrap: anywhere; }
  .acts { gap: var(--s-4); }
}
</style>
