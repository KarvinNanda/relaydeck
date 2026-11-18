<script setup lang="ts">
// The Messages timeline, newest first: time, from -> to, message, state.
// Rows that need the user carry their actions. Message text is written by
// agents, so it is rendered only through text interpolation.
import { reactive } from 'vue'
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
const errors = reactive(new Map<string, string>())

function toggle(id: string): void {
  if (open.has(id)) open.delete(id)
  else open.add(id)
}

async function act(m: Message, run: () => Promise<unknown>): Promise<void> {
  busy.add(m.id)
  errors.delete(m.id)
  try {
    await run()
  } catch (e) {
    errors.set(m.id, e instanceof ApiError ? e.message : 'the request failed')
  } finally {
    busy.delete(m.id)
  }
}

const approve = (m: Message) => act(m, () => props.messages.approve(m.id))
const reject = (m: Message) => act(m, () => props.messages.reject(m.id))
const startNew = (m: Message) => act(m, () => props.messages.startNew(m))
const deliverNow = (m: Message) => act(m, () => props.messages.deliverNow(m))
</script>

<template>
  <div class="tl" role="table" aria-label="Timeline, newest first">
    <div class="tr th" role="row">
      <span role="columnheader">time</span>
      <span role="columnheader">from -&gt; to</span>
      <span role="columnheader">message</span>
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
        <button class="msg" :class="{ open: open.has(m.id) }" type="button" :aria-expanded="open.has(m.id)" @click="toggle(m.id)">
          <Icon :name="open.has(m.id) ? 'down' : 'right'" />
          <span class="lbl">message: </span>
          <span class="body">{{ open.has(m.id) ? m.body : preview(m.body) }}</span>
        </button>
      </span>
      <span class="state" role="cell">
        <span class="st" :class="stateOf(m).cls"><Icon :name="stateOf(m).icon" />{{ stateOf(m).label(m) }}</span>
        <span v-if="m.state === 'awaiting_approval'" class="acts">
          <button class="btn btn-primary act" type="button" :disabled="busy.has(m.id)" @click="approve(m)">
            <Icon name="check" />Approve
          </button>
          <button class="btn btn-danger act" type="button" :disabled="busy.has(m.id)" @click="reject(m)">
            <Icon name="x" />Reject
          </button>
        </span>
        <span v-else-if="m.state === 'held_external' && !waited.has(m.id)" class="acts">
          <button class="btn act" type="button" :disabled="busy.has(m.id)" @click="startNew(m)">
            <Icon name="play" />Start new session
          </button>
          <button class="btn act" type="button" :disabled="busy.has(m.id)" @click="waited.add(m.id)">Wait</button>
        </span>
        <span v-else-if="m.state === 'held_draft'" class="acts">
          <button class="btn act" type="button" :disabled="busy.has(m.id)" @click="deliverNow(m)">
            <Icon name="right" />Deliver now
          </button>
        </span>
        <span v-if="errors.has(m.id)" class="err c-stop" role="alert"><Icon name="attn" />{{ errors.get(m.id) }}</span>
      </span>
    </div>
    <p v-if="messages.error.value" class="empty err-line c-stop" role="alert">
      <Icon name="attn" />Could not load the messages: {{ messages.error.value }}
      <button class="btn" type="button" @click="messages.reload()">Retry</button>
    </p>
    <p v-else-if="messages.newestFirst.value.length === 0" class="empty">
      {{ messages.loading.value ? 'Loading messages...' : 'No messages yet.' }}
    </p>
  </div>
</template>

<style scoped>
.tl { --inset: var(--s-6); overflow: auto; padding: var(--s-1) 0 var(--s-3); min-width: 0; }
.tr {
  display: grid; grid-template-columns: 64px 164px minmax(0, 1fr) 262px; gap: 0 var(--s-4);
  align-items: start; padding: var(--s-1) var(--inset); border-bottom: 1px solid var(--border);
}
.tr:last-of-type { border-bottom: 0; }
.tr.th { color: var(--text-muted); }
.tr.hot { background: var(--green-tint); }
.time { color: var(--text-muted); }
.route { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

.cell-msg { min-width: 0; }
.msg {
  display: flex; align-items: flex-start; gap: var(--s-2); width: 100%; min-width: 0;
  padding: 0; border: 0; border-radius: var(--r-sm); background: none; text-align: left; cursor: pointer;
}
.msg .i { color: var(--text-muted); margin-top: 2px; }
.msg:hover .i { color: var(--text); }
.msg .body { min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.msg.open .body { white-space: pre-wrap; overflow-wrap: anywhere; }
.lbl { display: none; color: var(--text-muted); }
/* An open message gets its own line under the row, across route, message
   and state, so long text is not squeezed into the message column. */
.state { grid-column: 4; grid-row: 1; }
.tr.open .cell-msg { grid-column: 2 / -1; grid-row: 2; padding: var(--s-1) 0 var(--s-2); }

.state { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s-2) var(--s-3); min-width: 0; }
.st, .err { display: inline-flex; align-items: center; gap: 5px; }
.err { align-items: flex-start; overflow-wrap: anywhere; }
.err .i { margin-top: 2px; }
.acts { display: flex; flex-wrap: wrap; gap: var(--s-3); width: 100%; }
.act:disabled { opacity: .55; cursor: progress; }

.empty { margin: 0; padding: var(--s-4) var(--inset); }
.err-line { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s-3); }

/* phone: each message is a card */
@media (max-width: 767px) {
  .tl { --inset: var(--gutter-mobile); overflow: visible; }
  .tr { grid-template-columns: auto minmax(0, 1fr); gap: var(--s-1) var(--s-5); padding: var(--s-4) var(--inset); }
  .tr.th { display: none; }
  .cell-msg, .state, .tr.open .cell-msg { grid-column: 1 / -1; grid-row: auto; padding: 0; }
  /* inline flow: wrapped text starts under the label, not beside it */
  .msg { display: block; min-height: var(--touch-h); padding: var(--s-5) 0; }
  .msg .i { margin: 0 var(--s-2) 0 0; vertical-align: -2px; }
  .msg .body { white-space: normal; overflow-wrap: anywhere; }
  .msg.open .body { white-space: pre-wrap; }
  .lbl { display: inline; }
}
</style>
